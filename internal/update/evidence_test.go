package update
import("crypto/sha256";"encoding/hex";"encoding/json";"fmt";"os";"path/filepath";"strings";"testing")
func evidenceFixture()Evidence{
 hash:=sha256.Sum256([]byte("agent"));m,_:=json.Marshal(Manifest{Channel:"test",Version:"0.0.0",Commit:strings.Repeat("a",40),Build:30,ReleaseID:"30-"+strings.Repeat("a",12)})
 mh:=sha256.Sum256(m);name:="hypermesh-agent_0.0.0_linux_arm64"
 return Evidence{Schema:1,Checksums:[]byte(fmt.Sprintf("%x  release.json\n%x  %s\n",mh,hash,name)),Signature:[]byte("signature"),Manifest:m,Binary:name,SHA256:hex.EncodeToString(hash[:])}
}
func TestSignedEvidenceBindsChannelBuildArchitectureAndBytes(t *testing.T){
 verify:=func([]byte,[]byte,[]byte,[]string)error{return nil}
 e:=evidenceFixture()
 if _,err:=e.Validate(nil,nil,"test","arm64",29,verify);err!=nil{t.Fatal(err)}
 for _,kind:=range []string{"channel","build","arch","manifest","binary","digest","signature","schema"}{t.Run(kind,func(t *testing.T){
  v:=e;ch,arch,min:="test","arm64",29;check:=verify
  switch kind{
  case "channel":ch="prod"
  case "build":min=31
  case "arch":arch="amd64"
  case "manifest":v.Manifest=[]byte("{}")
  case "binary":v.Binary="hypermesh-agent_0.0.0_linux_amd64"
  case "digest":v.SHA256=strings.Repeat("b",64)
  case "signature":check=func([]byte,[]byte,[]byte,[]string)error{return fmt.Errorf("untrusted")}
  case "schema":v.Schema=2
  }
  if _,err:=v.Validate(nil,nil,ch,arch,min,check);err==nil{t.Fatal("invalid update evidence accepted")}
 })}
}
func TestSignedEvidenceIsPrivateAndDoesNotFollowDirectorySymlinks(t *testing.T){
 dir:=t.TempDir();e:=evidenceFixture()
 if err:=WriteEvidence(dir,e);err!=nil{t.Fatal(err)}
 path:=filepath.Join(dir,"update-history",e.SHA256+".json")
 info,err:=os.Stat(path);if err!=nil||info.Mode().Perm()!=0600{t.Fatal("unsafe evidence")}
 raw,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
 var saved Evidence;if json.Unmarshal(raw,&saved)!=nil||saved.SHA256!=e.SHA256{t.Fatal("lost evidence")}
 other:=t.TempDir();if err=os.Symlink(filepath.Join(dir,"update-history"),filepath.Join(other,"update-history"));err!=nil{t.Fatal(err)}
 if WriteEvidence(other,e)==nil{t.Fatal("linked evidence directory accepted")}
}
