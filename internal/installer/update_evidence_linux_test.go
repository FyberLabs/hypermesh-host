//go:build linux

package installer

import (
 "context"
 "encoding/json"
 "fmt"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "testing"

 "github.com/FyberLabs/hypermesh-host/internal/update"
)

func TestInstalledAgentAcceptsOnlyItsSignedUpdateEvidence(t *testing.T) {
 p:=agentInstallFixture(t)
 root:=t.TempDir()
 home:=filepath.Join(root,"gpg")
 if err:=os.Mkdir(home,0700);err!=nil{t.Fatal(err)}
 gpg:=func(args ...string)[]byte{
  t.Helper()
  cmd:=exec.Command("gpg",append([]string{"--no-options","--homedir",home,"--batch"},args...)...)
  raw,err:=cmd.Output();if err!=nil{t.Fatal("fixture GPG failed",err)};return raw
 }
 gpg("--pinentry-mode","loopback","--passphrase","","--quick-generate-key","Lifecycle Test <test@example.invalid>","ed25519","sign","1d")
 t.Cleanup(func(){_ = exec.Command("gpgconf","--homedir",home,"--kill","all").Run()})
 for _,line:=range strings.Split(string(gpg("--with-colons","--list-keys")),"\n"){
  if strings.HasPrefix(line,"fpr:"){p.Fingerprints=[]string{strings.Split(line,":")[9]};break}
 }
 p.PublicKey=string(gpg("--armor","--export",p.Fingerprints[0]))
 p.Release.Build=21
 files:=agentPayloads(p,[]byte("verified agent"))
 p.Files=agentFileManifest(files);p.SHA256=agentPlanDigest(p)
 j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256}
 if err:=installAgentFiles(context.Background(),root,files,false,&j,func(agentInstallJournal)error{return nil});err!=nil{t.Fatal(err)}
 for _,path:=range []string{"/var/lib/hypermesh",defaultSetupState}{
  if err:=secureSetupDirectory(agentDest(root,path),true);err!=nil{t.Fatal(err)}
 }
 if err:=saveAgentJournal(agentDest(root,filepath.Join(defaultSetupState,"agent-"+p.SHA256+".json")),j);err!=nil{t.Fatal(err)}
 if err:=verifyInstalledForActivation(context.Background(),root,p,true);err!=nil{t.Fatal(err)}
 binary:=[]byte("verified newer agent")
 arch:=normalizedSetupArch(p.Setup.Preflight.Hardware.Facts.Arch)
 name:="hypermesh-agent_0.0.0_linux_"+arch
 meta,_:=json.Marshal(update.Manifest{Channel:"test",Version:"0.0.0",Build:22,Commit:strings.Repeat("b",40),ReleaseID:"22-"+strings.Repeat("b",12)})
 sums:=[]byte(fmt.Sprintf("%s  release.json\n%s  %s\n",digestSetupBytes(meta),digestSetupBytes(binary),name))
 sumPath:=filepath.Join(home,"checksums")
 if err:=os.WriteFile(sumPath,sums,0600);err!=nil{t.Fatal(err)}
 signature:=gpg("--pinentry-mode","loopback","--passphrase","","--detach-sign","--output","-",sumPath)
 evidence:=update.Evidence{Schema:1,Checksums:sums,Signature:signature,Manifest:meta,Binary:name,SHA256:digestSetupBytes(binary)}
 binaryPath:=agentDest(root,files[0].Path)
 if err:=recoveryWrite(binaryPath,binary,0755);err!=nil{t.Fatal(err)}
 if verifyInstalledForActivation(context.Background(),root,p,true)==nil{t.Fatal("accepted changed binary without evidence")}
 if err:=update.WriteEvidence(agentDest(root,"/var/lib/hypermesh"),evidence);err!=nil{t.Fatal(err)}
 if err:=verifyInstalledForActivation(context.Background(),root,p,true);err!=nil{t.Fatal("rejected valid signed update",err)}
 evidence.Signature=[]byte("invalid")
 if err:=update.WriteEvidence(agentDest(root,"/var/lib/hypermesh"),evidence);err!=nil{t.Fatal(err)}
 if verifyInstalledForActivation(context.Background(),root,p,true)==nil{t.Fatal("accepted invalid signature")}
 if err:=recoveryWrite(binaryPath,[]byte("verified agent"),0755);err!=nil{t.Fatal(err)}
 if err:=verifyInstalledForActivation(context.Background(),root,p,true);err!=nil{t.Fatal("original rollback no longer verifies",err)}
}
