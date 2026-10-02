//go:build linux
package installer
import("fmt";"path/filepath";"github.com/FyberLabs/hypermesh-host/internal/update")
func verifyInstalledUpdate(root string,p agentInstallPlan,sha string)error{
 if !setupDigestRE.MatchString(sha){return fmt.Errorf("invalid installed digest")}
 dir:=agentDest(root,"/var/lib/hypermesh/update-history")
 if err:=secureSetupDirectory(dir,false);err!=nil{return err}
 raw,err:=readSetupRegular(filepath.Join(dir,sha+".json"),true)
 var evidence update.Evidence
 if err==nil{err=decodeSetupJSON(raw,&evidence)}
 if err!=nil||evidence.SHA256!=sha{return fmt.Errorf("matching signed update evidence unavailable")}
 _,err=evidence.Validate([]byte(p.PublicKey),p.Fingerprints,p.Channel,normalizedSetupArch(p.Setup.Preflight.Hardware.Facts.Arch),int(p.Release.Build),update.GPGVerify)
 return err
}
