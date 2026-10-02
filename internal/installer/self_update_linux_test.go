//go:build linux
package installer
import("context";"os";"path/filepath";"runtime";"strings";"testing")
func installerUpdateFixture(t *testing.T)(installerUpdatePlan,string){
 t.Helper();dir:=t.TempDir();if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 path:=filepath.Join(dir,"deploy")
 if err:=os.WriteFile(path,[]byte("old installer"),0755);err!=nil{t.Fatal(err)}
 before,err:=recoverySnapshot("",path);if err!=nil{t.Fatal(err)}
 p:=installerUpdatePlan{Schema:1,Destination:path,Before:before,ReleaseURL:"https://example.invalid/test/release",Channel:"test",Arch:runtime.GOARCH,
 PublicKey:"-----BEGIN PGP PUBLIC KEY BLOCK-----\nfixture\n-----END PGP PUBLIC KEY BLOCK-----",Fingerprints:[]string{strings.Repeat("A",40)},
 Release:releaseMetadata{Channel:"test",Build:2,Version:"0.0.0",Commit:strings.Repeat("a",40)},PayloadSHA256:digestSetupBytes([]byte("new installer"))}
 p.SHA256=installerUpdateDigest(p);return p,dir
}
func TestInstallerUpdateAndExplicitRollbackPreserveBackup(t *testing.T){
 p,dir:=installerUpdateFixture(t)
 for i:=0;i<2;i++{if err:=installReviewedCLI(context.Background(),p,[]byte("new installer"),dir,false);err!=nil{t.Fatal(err)}}
 got,err:=os.ReadFile(p.Destination);if err!=nil||string(got)!="new installer"{t.Fatal("update failed")}
 backup,err:=os.ReadFile(filepath.Join(dir,"previous"));if err!=nil||string(backup)!="old installer"{t.Fatal("backup lost")}
 for i:=0;i<2;i++{if err:=installReviewedCLI(context.Background(),p,nil,dir,true);err!=nil{t.Fatal(err)}}
 got,err=os.ReadFile(p.Destination);if err!=nil||string(got)!="old installer"{t.Fatal("rollback failed")}
}
func TestInstallerUpdateRejectsChangedOrUnsafeAuthority(t *testing.T){
 for _,kind:=range []string{"payload","current","symlink","backup","approval","cancel"}{t.Run(kind,func(t *testing.T){
  p,dir:=installerUpdateFixture(t);raw:=[]byte("new installer");ctx,cancel:=context.WithCancel(context.Background());defer cancel()
  switch kind{
  case "payload":raw=[]byte("wrong")
  case "current":if err:=os.WriteFile(p.Destination,[]byte("other"),0755);err!=nil{t.Fatal(err)}
  case "symlink":if err:=os.Remove(p.Destination);err!=nil{t.Fatal(err)};if err:=os.Symlink("/private",p.Destination);err!=nil{t.Fatal(err)}
  case "backup":if err:=os.WriteFile(filepath.Join(dir,"previous"),[]byte("other"),0600);err!=nil{t.Fatal(err)}
  case "approval":p.Channel="prod"
  case "cancel":cancel()
  }
  if installReviewedCLI(ctx,p,raw,dir,false)==nil{t.Fatal("unsafe update accepted")}
 })}
}
