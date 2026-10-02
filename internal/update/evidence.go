package update

import (
 "encoding/json"
 "fmt"
 "os"
 "path/filepath"
)
// Evidence retains public, signed release material, never artifact URLs or SAS tokens.
// Installed hashes are reverified against this proof by the installer.
type Evidence struct{
 Schema int `json:"schema"`
 Checksums []byte `json:"checksums"`
 Signature []byte `json:"signature"`
 Manifest []byte `json:"manifest"`
 Binary string `json:"binary"`
 SHA256 string `json:"sha256"`
}
func (e Evidence) Validate(pub []byte,fingerprints []string,channel,arch string,minimum int,verify func([]byte,[]byte,[]byte,[]string)error)(Manifest,error){
 var m Manifest
 if e.Schema!=1||len(e.Checksums)==0||len(e.Checksums)>512<<10||len(e.Signature)==0||len(e.Signature)>128<<10||len(e.Manifest)>64<<10||!sha256Hex(e.SHA256){return m,fmt.Errorf("invalid signed update evidence")}
 if err:=verify(e.Checksums,e.Signature,pub,fingerprints);err!=nil{return m,err}
 if err:=CheckHash(e.Checksums,ManifestName,e.Manifest);err!=nil{return m,err}
 if json.Unmarshal(e.Manifest,&m)!=nil||m.Channel!=channel||m.Build<minimum||m.Build<=0{return m,fmt.Errorf("signed update channel/build mismatch")}
 name,err:=SelectBinary(string(e.Checksums),arch);if err!=nil||name!=e.Binary{return m,fmt.Errorf("signed update architecture mismatch")}
 hash,err:=checksumFor(string(e.Checksums),name);if err!=nil||hash!=e.SHA256{return m,fmt.Errorf("signed update binary hash mismatch")}
 return m,nil
}
func WriteEvidence(stateDir string,e Evidence)error{
 if !sha256Hex(e.SHA256){return fmt.Errorf("invalid evidence digest")}
 dir:=filepath.Join(stateDir,"update-history")
 if err:=os.Mkdir(dir,0700);err!=nil&&!os.IsExist(err){return err}
 info,err:=os.Lstat(dir);if err!=nil||!info.IsDir()||info.Mode().Perm()!=0700{return fmt.Errorf("unsafe update evidence directory")}
 path:=filepath.Join(dir,e.SHA256+".json")
 raw,err:=json.Marshal(e);if err!=nil||len(raw)>1<<20{return fmt.Errorf("update evidence too large")}
 f,err:=os.CreateTemp(dir,".evidence-");if err!=nil{return err};defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return err}
 if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err}
 if err=os.Rename(f.Name(),path);err!=nil{return err}
 return syncDir(path)
}
