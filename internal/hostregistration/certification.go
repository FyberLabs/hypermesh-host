package hostregistration

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "regexp"
 "math"
 "time"
)

type CertificationManifest struct {
 Purpose string `json:"purpose,omitempty"`
 Repetitions int `json:"repetitions,omitempty"`
 SuiteID string `json:"suite_id"`
 CatalogID string `json:"catalog_id"`
 ClassID string `json:"class_id"`
 FileHash string `json:"file_hash"`
 ImageDigest string `json:"image_digest"`
 SourceRef string `json:"source_ref"`
 SizeBytes int64 `json:"size_bytes"`
 Runtime string `json:"runtime"`
 Backend string `json:"backend"`
 ContextTokens int `json:"context_tokens"`
 FullContext bool `json:"full_context"`
 DecodeTokS float64 `json:"decode_tok_s"`
 ThermalC *float64 `json:"thermal_c"`
}
type CertificationRun struct {
 ProtocolVersion int `json:"protocol_version"`
 Nonce string `json:"nonce"`
 RunID string `json:"run_id"`
 TenantID string `json:"tenant_id"`
 DeviceID string `json:"device_id"`
 RequestID string `json:"request_id"`
 Manifest CertificationManifest `json:"manifest"`
 ManifestSHA256 string `json:"manifest_sha256"`
 State string `json:"state"`
 ExpiresAt time.Time `json:"expires_at"`
 SellingAllowed *bool `json:"selling_allowed"`
 ActivationAllowed *bool `json:"activation_allowed,omitempty"`
 MaintenanceResume bool `json:"maintenance_resume,omitempty"`
 PermitExpiresAt *time.Time `json:"permit_expires_at,omitempty"`
}
type CertificationEvidence struct {
 HostImageHash string `json:"host_image_hash"`
 FileHash string `json:"file_hash"`
 ImageDigest string `json:"image_digest"`
 SampleMS int `json:"sample_ms"`
 TokensPerSecond float64 `json:"tokens_per_second"`
 ThermalC *float64 `json:"thermal_c"`
 ContextTokens int `json:"context_tokens"`
 PromptTokens int `json:"prompt_tokens"`
 GeneratedTokens int `json:"generated_tokens"`
 ContextElapsedMS int64 `json:"context_elapsed_ms"`
 CleanupComplete bool `json:"cleanup_complete"`
 TunnelVerified bool `json:"tunnel_verified"`
}
func NewRunID()(string,error){
 b:=make([]byte,16);if _,err:=rand.Read(b);err!=nil{return "",fmt.Errorf("cannot create run identity")}
 b[6]=(b[6]&15)|64;b[8]=(b[8]&63)|128
 return fmt.Sprintf("%x-%x-%x-%x-%x",b[:4],b[4:6],b[6:8],b[8:10],b[10:]),nil
}
func (m CertificationManifest) Validate()error{
 if m.Purpose!=""||m.Repetitions!=0{return fmt.Errorf("qualification is not host certification")}
 return m.validatePins(true)
}
func (m CertificationManifest) ValidateQualification()error{
 if m.Purpose!="lab-qualification-v1"||m.Repetitions!=3||m.DecodeTokS!=0||m.ThermalC!=nil{return fmt.Errorf("invalid qualification purpose or measurement policy")}
 return m.validatePins(false)
}
func (m CertificationManifest) validatePins(baseline bool)error{
 if !oneOf(m.SuiteID,"pack-v1","thin-v1")||!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(m.CatalogID)||
 !oneOf(m.ClassID,"agx-large","nx-volume")||
 !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(m.FileHash)||
 !regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`).MatchString(m.ImageDigest)||
 m.SizeBytes<=0||m.SizeBytes>512<<30||m.Runtime!="nvidia"||m.Backend!="cuda-jetson"||
 m.ContextTokens<512||m.ContextTokens>131072||!m.FullContext||(baseline&&m.DecodeTokS<=0)||math.IsNaN(m.DecodeTokS)||math.IsInf(m.DecodeTokS,0)||
 (m.ThermalC!=nil&&(math.IsNaN(*m.ThermalC)||math.IsInf(*m.ThermalC,0))){
 return fmt.Errorf("incomplete or unsupported certification manifest")
 }
 return nil
}
func SameManifest(a,b CertificationManifest)bool{
 x,_:=json.Marshal(a);y,_:=json.Marshal(b);return string(x)==string(y)
}
func (c *Client) certification(ctx context.Context,r Request,device,secret,runID,op string,previous *CertificationRun,evidence *CertificationEvidence)(CertificationRun,error){
 var out CertificationRun
 if r.Validate()!=nil||!UUID.MatchString(device)||!UUID.MatchString(runID)||!onboardingSecretRE.MatchString(secret){return out,fmt.Errorf("invalid certification identity")}
 b:=make([]byte,32);if _,err:=rand.Read(b);err!=nil{return out,fmt.Errorf("cannot create challenge")}
 nonce:=hex.EncodeToString(b)
 body:=map[string]any{"protocol_version":1,"request_id":r.RequestID,"device_id":device,"run_id":runID,"nonce":nonce}
 if previous!=nil{body["manifest_sha256"]=previous.ManifestSHA256}
 if evidence!=nil{body["evidence"]=evidence}
 scoped:=*c;scoped.timeout=30*time.Second
 if err:=scoped.request(ctx,"POST","/api/v1/hypermesh/agent/onboarding/certification/"+op,secret,body,&out);err!=nil{return out,err}
 if out.ProtocolVersion!=1||out.Nonce!=nonce||out.RunID!=runID||out.TenantID!=c.tenant||
 out.RequestID!=r.RequestID||out.DeviceID!=device||out.Manifest.ClassID!=r.ClassID||
 !hashRE.MatchString(out.ManifestSHA256)||out.Manifest.Validate()!=nil||
 !oneOf(out.State,"planned","passed","failed")||out.ExpiresAt.IsZero()||
 out.SellingAllowed==nil||*out.SellingAllowed{return CertificationRun{},fmt.Errorf("certification response does not match this installation")}
 if previous!=nil&&(out.ManifestSHA256!=previous.ManifestSHA256||!SameManifest(out.Manifest,previous.Manifest)){return CertificationRun{},fmt.Errorf("certification workload changed")}
 if op=="activation"||op=="resume"{
  if out.State!="passed"||out.ActivationAllowed==nil||!*out.ActivationAllowed||
  out.PermitExpiresAt==nil||!out.PermitExpiresAt.After(time.Now())||
  (op=="activation"&&out.PermitExpiresAt.After(out.ExpiresAt))||(op=="resume"&&!out.MaintenanceResume)||out.PermitExpiresAt.After(time.Now().Add(3*time.Minute)){return CertificationRun{},fmt.Errorf("activation permit unavailable or expired")}
 }
 return out,nil
}
func(c *Client) PlanCertification(ctx context.Context,r Request,device,secret,id string)(CertificationRun,error){
 return c.certification(ctx,r,device,secret,id,"plan",nil,nil)
}
func(c *Client) ReportCertification(ctx context.Context,r Request,device,secret string,run CertificationRun,e CertificationEvidence)(CertificationRun,error){
 return c.certification(ctx,r,device,secret,run.RunID,"result",&run,&e)
}
func(c *Client) ActivationPermit(ctx context.Context,r Request,device,secret string,run CertificationRun)(CertificationRun,error){
 return c.certification(ctx,r,device,secret,run.RunID,"activation",&run,nil)
}
func(c *Client) ResumePermit(ctx context.Context,r Request,device,secret string,run CertificationRun)(CertificationRun,error){
 return c.certification(ctx,r,device,secret,run.RunID,"resume",&run,nil)
}
func(c *Client) ApproveActivation(ctx context.Context,run CertificationRun)error{return c.approveStart(ctx,run,"approve-activation")}
func(c *Client) ApproveResume(ctx context.Context,run CertificationRun)error{return c.approveStart(ctx,run,"approve-resume")}
func(c *Client) approveStart(ctx context.Context,run CertificationRun,operation string)error{
 if c.bearer==""{return fmt.Errorf("original owner login required")}
 var out struct{RunID string `json:"run_id"`;ManifestSHA256 string `json:"manifest_sha256"`;Approved bool `json:"approved"`}
 if err:=c.request(ctx,"POST","/api/v1/hypermesh/host-onboarding/"+operation,c.bearer,
 map[string]string{"run_id":run.RunID,"manifest_sha256":run.ManifestSHA256},&out);err!=nil{return err}
 if !out.Approved||out.RunID!=run.RunID||out.ManifestSHA256!=run.ManifestSHA256{return fmt.Errorf("owner activation approval unconfirmed")}
 return nil
}
