//go:build linux

package installer

import (
 "context"
 "fmt"
 "os"
 "path/filepath"
 "sort"
)

type platformReviewPrompt struct{}
type platformReviewOutput struct{}
type installedPlatformReview struct {
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 Machine string `json:"machine"`
 Packages []PackageChange `json:"packages"`
}
func platformReviewPath(p agentInstallPlan)string{return filepath.Join(defaultSetupState,"platform-review-"+p.SHA256+".json")}
func reviewedRegistrationSetup(p agentInstallPlan)(SetupPlan,error){
 setup:=p.Setup
 path:=platformReviewPath(p)
 if _,err:=os.Lstat(path);os.IsNotExist(err){return setup,nil}else if err!=nil{return setup,err}
 var review installedPlatformReview
 if err:=guidedRead(path,&review,true);err!=nil{return setup,err}
 machine,_,err:=rebootIdentity("")
 if err!=nil{return setup,err}
 if review.Schema!=1||review.PlanSHA256!=p.SHA256||review.Machine!=machine{return setup,fmt.Errorf("platform review does not match this installation")}
 setup.Platform.ProtectedPackages=review.Packages
 return setup,nil
}

// Review existing version changes, never perform package mutations or silently
// accept a removed driver. Original installation manifests remain unchanged.
func updatedPlatformPackages(setup SetupPlan,installed map[string]string)([]PackageChange,[]string,error){
 packages:=append([]PackageChange(nil),setup.Platform.ProtectedPackages...)
 sort.Slice(packages,func(i,j int)bool{return packages[i].Name<packages[j].Name})
 changes:=[]string{}
 for i,pkg:=range packages{
  version:=installed[pkg.Name]
  if version==""{return nil,nil,fmt.Errorf("protected package %s is missing; restore the vendor package before continuing",pkg.Name)}
  if version!=pkg.To{changes=append(changes,fmt.Sprintf("%s: %s -> %s",pkg.Name,pkg.To,version));packages[i]=PackageChange{pkg.Name,version,version}}
 }
 return packages,changes,nil
}
func reviewRegistrationPlatform(ctx context.Context,p agentInstallPlan,class string,confirm func(string)error,display func(string))error{
 setup,err:=reviewedRegistrationSetup(p);if err!=nil{return err}
 installed,err:=readSetupInventory(ctx,setupSource{});if err!=nil{return err}
 return reviewInstalledVersions(setup,installed,func(candidate SetupPlan)error{
  _,err:=registrationPreflightSetup(ctx,p,class,candidate);return err
 },confirm,display,func(packages []PackageChange)error{
  machine,_,err:=rebootIdentity("");if err!=nil{return err}
  review:=installedPlatformReview{1,p.SHA256,machine,packages}
  return writeGuided(platformReviewPath(p),review)
 })
}
func reviewInstalledVersions(setup SetupPlan,installed map[string]string,validate func(SetupPlan)error,confirm func(string)error,display func(string),save func([]PackageChange)error)error{
 packages,changes,err:=updatedPlatformPackages(setup,installed);if err!=nil{return err}
 candidate:=setup;candidate.Platform.ProtectedPackages=packages
 if err=validate(candidate);err!=nil{return err}
 if len(changes)==0{return nil}
 display("Installed platform packages changed since your previous review:")
 for _,change:=range changes{display(" - "+change)}
 if confirm==nil{return fmt.Errorf("platform updates need review; rerun register-agent with --review-platform-updates, or resume guided setup")}
 if err=confirm("Accept these already-installed package versions and continue? No packages will be changed.");err!=nil{return err}
 // Recheck after the user responds; never acknowledge a different inventory.
 if err=validate(candidate);err!=nil{return err}
 return save(packages)
}
