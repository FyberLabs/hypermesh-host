//go:build linux

package installer

import (
 "context"
 "crypto/sha256"
 "fmt"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "regexp"
 "strings"
 "syscall"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/pull"
 "github.com/FyberLabs/hypermesh-host/internal/agent"
)

var certificationSourceRE=regexp.MustCompile(`(?i)^(bartowski|TheBloke|QuantFactory|ggml-org|lmstudio-community|FyberLabs)/[a-z0-9._-]+@[a-f0-9]{40}/[a-z0-9._/-]+\.gguf$`)
func certificationURL(ref string)(string,error){
 if len(ref)>1024||!certificationSourceRE.MatchString(ref){return "",fmt.Errorf("model source requires an approved publisher and immutable commit")}
 for _,part:=range strings.Split(ref,"/"){if part==""||part=="."||part==".."{return "",fmt.Errorf("unsafe model source")}}
 return pull.TrustedDownloadURL(ref)
}
func modelTransport(token string)*http.Client{
 return &http.Client{Timeout:30*time.Minute,Transport:&http.Transport{
  Proxy:nil,ResponseHeaderTimeout:30*time.Second,TLSHandshakeTimeout:15*time.Second,
 },CheckRedirect:func(req *http.Request,via []*http.Request)error{
  host:=strings.ToLower(req.URL.Hostname())
  if len(via)>8||req.URL.Scheme!="https"||req.URL.User!=nil||req.URL.Port()!=""||
   !(host=="huggingface.co"||host=="hf.co"||strings.HasSuffix(host,".hf.co")||strings.HasSuffix(host,".huggingface.co")){
   return fmt.Errorf("untrusted model redirect")
  }
  req.Header.Del("Authorization")
  if host=="huggingface.co"&&token!=""{req.Header.Set("Authorization","Bearer "+token)}
  return nil
 }}
}
func modelBytes(ctx context.Context,client *http.Client,address,token string)(io.ReadCloser,error){
 req,err:=http.NewRequestWithContext(ctx,"GET",address,nil);if err!=nil{return nil,fmt.Errorf("invalid model request")}
 req.Header.Set("Accept-Encoding","identity")
 if token!=""{req.Header.Set("Authorization","Bearer "+token)}
 response,err:=client.Do(req);if err!=nil{return nil,fmt.Errorf("model transfer failed or timed out")}
 if response.StatusCode!=200||response.Header.Get("Content-Encoding")!=""{
  response.Body.Close();return nil,fmt.Errorf("model transfer rejected; check source access")}
 return response.Body,nil
}
func verifyAcquiredModel(ctx context.Context,path,pin string,size int64)error{
 info,err:=os.Lstat(path)
 if err!=nil||!info.Mode().IsRegular(){return fmt.Errorf("model file missing or unsafe")}
 stat,ok:=info.Sys().(*syscall.Stat_t)
 if !ok||stat.Nlink!=1||stat.Uid!=uint32(os.Geteuid())||info.Mode().Perm()&0022!=0||info.Size()!=size{return fmt.Errorf("model size, ownership or permissions differ")}
 f,err:=os.OpenFile(path,os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,0);if err!=nil{return err};defer f.Close()
 after,err:=f.Stat();if err!=nil||!os.SameFile(info,after){return fmt.Errorf("model changed")}
 h:=sha256.New();buf:=make([]byte,1<<20)
 for {if ctx.Err()!=nil{return fmt.Errorf("model verification interrupted")};n,e:=f.Read(buf);if n>0{h.Write(buf[:n])};if e==io.EOF{break};if e!=nil{return fmt.Errorf("model verification failed")}}
 if fmt.Sprintf("sha256:%x",h.Sum(nil))!=pin{return fmt.Errorf("model hash differs from approved manifest")}
 return nil
}
func acquireModel(ctx context.Context,path,pin string,size int64,fetch func(context.Context)(io.ReadCloser,error))error{
 return agent.AcquireCachedModel(ctx,path,pin,size,func()error{return acquireModelFile(ctx,path,pin,size,fetch)})
}
func acquireModelFile(ctx context.Context,path,pin string,size int64,fetch func(context.Context)(io.ReadCloser,error))error{
 if err:=agentDirectory(filepath.Dir(path),false);err!=nil{return err}
 if _,err:=os.Lstat(path);err==nil{return verifyAcquiredModel(ctx,path,pin,size)}else if !os.IsNotExist(err){return fmt.Errorf("cannot inspect model destination")}
 if err:=pull.Preflight(path,size,pull.Limits{Expected:size});err!=nil{return fmt.Errorf("insufficient space on models filesystem for approved download and reserve")}
 body,err:=fetch(ctx);if err!=nil{return err};defer body.Close()
 f,err:=os.CreateTemp(filepath.Dir(path),".certification-model-");if err!=nil{return err}
 defer os.Remove(f.Name())
 h:=sha256.New()
 n,copyErr:=io.Copy(io.MultiWriter(f,h),io.LimitReader(body,size+1))
 if copyErr!=nil||ctx.Err()!=nil||n!=size||fmt.Sprintf("sha256:%x",h.Sum(nil))!=pin{
  f.Close();return fmt.Errorf("model transfer incomplete, oversized or hash mismatched")
 }
 if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err}
 if err=os.Link(f.Name(),path);err!=nil{return fmt.Errorf("model destination appeared; preserve it and retry verification")}
 if err=os.Remove(f.Name());err!=nil{return err}
 d,err:=os.Open(filepath.Dir(path));if err!=nil{return err};defer d.Close()
 return d.Sync()
}
// Secrets stay in memory, never in plans, logs, URLs or process arguments.
func modelAccessToken(path string)(string,error){
 if path==""{return "",nil}
 raw,err:=readSetupRegular(path,true);if err!=nil{return "",fmt.Errorf("cannot read private model access token")}
 token:=strings.TrimSpace(string(raw))
 if !regexp.MustCompile(`^[A-Za-z0-9_-]{8,512}$`).MatchString(token){return "",fmt.Errorf("invalid model access token")}
 return token,nil
}
