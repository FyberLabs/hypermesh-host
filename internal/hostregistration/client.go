// Package hostregistration implements browser-approved, tenant-scoped host registration.
package hostregistration

import (
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/base64"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "net/url"
 "regexp"
 "strings"
 "time"
)

var UUID = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
var nameRE = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$")
var hashRE = regexp.MustCompile("^[a-f0-9]{64}$")
var prefixRE = regexp.MustCompile("^[A-Za-z0-9_-]{8}$")

type Hardware struct {
 Profile string `json:"profile"`
 Architecture string `json:"architecture"`
 CPU string `json:"cpu"`
 GPU string `json:"gpu"`
 SystemMemoryBytes int64 `json:"system_memory_bytes"`
 ModelMemoryBytes int64 `json:"model_memory_bytes"`
}
type Request struct {
 RequestID string `json:"request_id"`
 HostName string `json:"host_name"`
 ClassID string `json:"class_id"`
 Hardware Hardware `json:"hardware"`
 WGPublicKey string `json:"wg_public_key"`
 DeviceSecretHash string `json:"device_secret_hash"`
 CredentialPrefix string `json:"credential_prefix"`
}
type Status struct {
 RequestID string `json:"request_id"`
 TenantID string `json:"tenant_id"`
 State string `json:"state"`
 HostName string `json:"host_name"`
 ClassID string `json:"class_id"`
 Hardware Hardware `json:"hardware"`
 KeyFingerprint string `json:"key_fingerprint"`
 ExpiresAt string `json:"expires_at"`
 DeviceID *string `json:"device_id"`
 ApprovalPath string `json:"approval_path"`
 NextAction string `json:"next_action"`
}
type Client struct {
 base, tenant, bearer string
 http *http.Client
 timeout time.Duration
}
func (Client) String() string { return "[redacted registration client]" }
func (Client) GoString() string { return "[redacted registration client]" }
func (Client) MarshalJSON() ([]byte,error) { return []byte("\"[redacted registration client]\""),nil }

// BaseURL preserves the path prefix accepted by installed agent plans.
func BaseURL(s string) bool {
 u,e:=url.Parse(s)
 if e!=nil || u.Scheme!="https" || u.Hostname()=="" || u.User!=nil || u.Opaque!="" || u.RawQuery!="" || u.ForceQuery || u.Fragment!="" {return false}
 for _,r:=range s {if r<32 || r>126 {return false}}
 return true
}
// Dashboard links are relative to an explicitly configured origin.
func Origin(s string) bool {
 if !BaseURL(s){return false}
 u,_:=url.Parse(s)
 return (u.Path==""||u.Path=="/")&&u.RawPath==""
}
func New(base,tenant string)(*Client,error) {
 tenant=strings.ToLower(tenant)
 if !BaseURL(base) || !UUID.MatchString(tenant) {return nil,fmt.Errorf("registration requires an HTTPS base URL and tenant UUID")}
 return &Client{base:strings.TrimRight(base,"/"),tenant:tenant,http:&http.Client{},timeout:15*time.Second},nil
}
func (c *Client) request(ctx context.Context,method,path,token string,body,dest any) error {
 ctx,cancel:=context.WithTimeout(ctx,c.timeout);defer cancel()
 var raw []byte
 var err error
 if body!=nil {raw,err=json.Marshal(body);if err!=nil{return fmt.Errorf("cannot encode registration request")}}
 req,err:=http.NewRequestWithContext(ctx,method,c.base+path,bytes.NewReader(raw))
 if err!=nil{return fmt.Errorf("cannot construct registration request")}
 req.Header.Set("Authorization","Bearer "+token)
 req.Header.Set("X-Tenant-ID",c.tenant)
 req.Header.Set("Content-Type","application/json")
 client:=*c.http
 client.CheckRedirect=func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}
 resp,err:=client.Do(req)
 if err!=nil{return fmt.Errorf("registration transport failed or timed out; rerun to recover")}
 defer resp.Body.Close()
 if resp.StatusCode<200 || resp.StatusCode>=300 {
  action:="request failed; retry the same command after checking the plane"
  switch resp.StatusCode {
  case 401:action="session expired or invalid; rerun and sign in again"
  case 403:action="access denied; verify the original owner, tenant and host membership"
  case 404:action="registration API or owned request unavailable; verify deployment and environment"
  case 409:action="class, host name, key or state conflict; inspect the dashboard and platform class configuration"
  case 410:action="host retired; use the explicit fresh-key reinstall workflow"
  case 429:action="rate limited; wait before retrying the same command"
  case 503:action="plane temporarily unavailable; retry the same command"
  }
  return fmt.Errorf("registration API returned HTTP %d: %s; request and keys retained",resp.StatusCode,action)
 }
 raw,err=io.ReadAll(io.LimitReader(resp.Body,(1<<20)+1))
 if err!=nil || len(raw)>1<<20 || ctx.Err()!=nil {return fmt.Errorf("registration response unavailable, too large or timed out")}
 if json.Unmarshal(raw,dest)!=nil {return fmt.Errorf("invalid registration response")}
 return nil
}
// Login verifies ownership before any mutation. bind must durably record or
// compare the authenticated subject; browser/device tokens never enter journals.
func (c *Client) Login(ctx context.Context,token string,bind func(string)error) error {
 var me struct {Sub string `json:"sub"`}
 if err:=c.request(ctx,"GET","/api/v1/auth/me",token,nil,&me);err!=nil{return err}
 if me.Sub=="" || len(me.Sub)>256 {return fmt.Errorf("verified login has no valid subject")}
 if err:=bind(me.Sub);err!=nil{return err}
 var grant struct {Granted []string `json:"granted"`; Refused []string `json:"refused"`; DeviceEnrolled bool `json:"device_enrolled"`}
 if err:=c.request(ctx,"POST","/api/v1/auth/enrollment/grant",token,map[string]any{"tenant_id":c.tenant,"held_roles":[]string{"host"}},&grant);err!=nil{return err}
 host:=false;for _,r:=range grant.Granted {host=host||r=="host"}
 if !host || len(grant.Refused)>0 || grant.DeviceEnrolled {return fmt.Errorf("host organization membership was not granted")}
 var session struct {AccessToken string `json:"access_token"`;TokenType string `json:"token_type"`;ExpiresIn int `json:"expires_in"`}
 if err:=c.request(ctx,"POST","/api/v1/auth/switch-tenant",token,map[string]string{"tenant_id":c.tenant},&session);err!=nil{return err}
 if session.AccessToken=="" || !strings.EqualFold(session.TokenType,"Bearer") || session.ExpiresIn<=0 {return fmt.Errorf("tenant session unavailable")}
 c.bearer=session.AccessToken
 return nil
}
func printable(s string,max int)bool {
 if len(s)==0||len(s)>max{return false}
 for _,r:=range s {if r<32||r==127{return false}}
 return true
}
func (r Request) Validate()error {
 h:=r.Hardware
 raw,e:=base64.StdEncoding.DecodeString(r.WGPublicKey)
 if !UUID.MatchString(r.RequestID)||!nameRE.MatchString(r.HostName)||!nameRE.MatchString(r.ClassID)||
 !printable(h.Profile,64)||!printable(h.CPU,128)||!printable(h.GPU,128)||
 (h.Architecture!="arm64"&&h.Architecture!="amd64")||h.SystemMemoryBytes<=0||h.SystemMemoryBytes>1<<60||h.ModelMemoryBytes<=0||h.ModelMemoryBytes>1<<60||
 e!=nil||len(raw)!=32||base64.StdEncoding.EncodeToString(raw)!=r.WGPublicKey||bytes.Equal(raw,make([]byte,32))||!hashRE.MatchString(r.DeviceSecretHash)||!prefixRE.MatchString(r.CredentialPrefix) {return fmt.Errorf("invalid local registration identity or hardware report")}
 return nil
}
func Fingerprint(key string)string {
 raw,_:=base64.StdEncoding.DecodeString(key);sum:=sha256.Sum256(raw);return hex.EncodeToString(sum[:])
}
func (c *Client) check(s Status,r Request)error {
 if s.RequestID!=r.RequestID||s.TenantID!=c.tenant||s.HostName!=r.HostName||s.ClassID!=r.ClassID||s.Hardware!=r.Hardware||s.KeyFingerprint!=Fingerprint(r.WGPublicKey)||s.ApprovalPath!="/dashboard/hypermesh/host/approve/"+r.RequestID {return fmt.Errorf("registration response does not match this installation")}
 if _,err:=time.Parse(time.RFC3339Nano,s.ExpiresAt);err!=nil{return fmt.Errorf("invalid registration expiry")}
 switch s.State {case "pending","approved","denied","expired":
 if s.DeviceID!=nil{return fmt.Errorf("unexpected device identity before completion")}
 case "completed","retired":
 if s.DeviceID==nil||!UUID.MatchString(*s.DeviceID){return fmt.Errorf("missing completed device identity")}
 default:return fmt.Errorf("unknown registration state")}
 return nil
}
func (c *Client) operation(ctx context.Context,method,suffix string,r Request,body any)(Status,error) {
 var s Status
 if err:=r.Validate();err!=nil{return s,err}
 if c.bearer=="" {return s,fmt.Errorf("tenant login required")}
 if err:=c.request(ctx,method,"/api/v1/hypermesh/host-registrations"+suffix,c.bearer,body,&s);err!=nil{return s,err}
 return s,c.check(s,r)
}
func (c *Client) Create(ctx context.Context,r Request)(Status,error){return c.operation(ctx,"POST","",r,r)}
func (c *Client) Get(ctx context.Context,r Request)(Status,error){return c.operation(ctx,"GET","/"+r.RequestID,r,nil)}
func (c *Client) Complete(ctx context.Context,r Request)(Status,error){return c.operation(ctx,"POST","/"+r.RequestID+"/complete",r,nil)}
