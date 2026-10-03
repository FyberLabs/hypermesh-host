// Package hardware describes capabilities, never enrollment identity.
package hardware

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "strconv"
    "strings"
    "time"
)

type Facts struct {
    Arch string `json:"arch"`
    Model string `json:"model"`
    Processor string `json:"processor"`
    GPU string `json:"gpu"`
    MemoryBytes int64 `json:"memory_bytes"`
    AvailableBytes int64 `json:"available_bytes"`
    AcceleratorFreeBytes int64 `json:"accelerator_free_bytes"`
    Tegra bool `json:"tegra"`
    CUDA bool `json:"cuda"`
    HIP bool `json:"hip"`
    Vulkan bool `json:"vulkan"`
    Metal bool `json:"metal"`
}

type Profile struct {
    Fingerprint string `json:"fingerprint"`
    Family string `json:"family"`
    Facts Facts `json:"facts"`
    Backend string `json:"backend"`
    Libraries []string `json:"libraries"`
    Setup []string `json:"setup"`
    ModelBudgetBytes int64 `json:"model_budget_bytes"`
    BenchmarkArgs []string `json:"benchmark_args"`
    Notes []string `json:"notes"`
}

func Recommend(f Facts) Profile {
    p := Profile{Facts:f, Family:"generic", Backend:"cpu",
        Libraries:[]string{"llama.cpp CPU"}, Setup:[]string{"CMake", "C/C++ compiler"},
        BenchmarkArgs:[]string{"-p","512","-n","128","-ngl","0"},
        Notes:[]string{"Recommendations are not certification or measured throughput.",
            "Model budget reserves memory for runtime and context; benchmark the actual model."}}
    model := strings.ToLower(f.Model)
    switch {
    case f.Tegra && strings.Contains(model,"agx"):
        p.Family = "nvidia-jetson-agx"
    case f.Tegra:
        p.Family = "nvidia-jetson"
    case strings.Contains(model,"dgx"):
        p.Family = "nvidia-dgx"
    case f.Metal && (f.Arch=="arm64" || f.Arch=="aarch64"):
        p.Family = "apple-silicon"
    }
    memory := f.AvailableBytes
    switch {
    case f.CUDA:
        p.Backend = "cuda"
        p.Libraries = []string{"llama.cpp CUDA"}
        p.Setup = []string{"Compatible NVIDIA driver and CUDA toolkit", "CMake -DGGML_CUDA=ON"}
        if f.Tegra {
            p.Setup = append(p.Setup, "Use the vendor JetPack/L4T toolchain for this module")
        } else if f.AcceleratorFreeBytes > 0 && f.AcceleratorFreeBytes < memory {
            memory = f.AcceleratorFreeBytes
        } else if f.AcceleratorFreeBytes == 0 {
            memory = 0
        }
    case f.HIP:
        p.Backend = "hip"
        p.Libraries = []string{"llama.cpp HIP"}
        p.Setup = []string{"Verify this GPU is supported by the installed ROCm release", "CMake -DGGML_HIP=ON"}
        if f.AcceleratorFreeBytes == 0 { memory = 0
        } else if f.AcceleratorFreeBytes < memory { memory = f.AcceleratorFreeBytes }
    case f.Metal:
        p.Backend = "metal"
        p.Libraries = []string{"llama.cpp Metal", "Accelerate"}
        p.Setup = []string{"Xcode command-line tools", "CMake -DGGML_METAL=ON"}
    case f.Vulkan:
        p.Backend = "vulkan"
        p.Libraries = []string{"llama.cpp Vulkan"}
        p.Setup = []string{"Vendor Vulkan driver and SDK", "CMake -DGGML_VULKAN=ON"}
        // Vulkan presence alone does not establish an allocation budget.
        memory = 0
    }
    if p.Backend != "cpu" { p.BenchmarkArgs[5] = "99" }
    reserve := memory/4
    if reserve < 2<<30 { reserve = 2<<30 }
    if memory > reserve { p.ModelBudgetBytes = memory-reserve }
    if p.ModelBudgetBytes == 0 {
        p.Notes = append(p.Notes,"Available accelerator/host memory is unknown or insufficient; measure it before benchmarking.")
    }
    // Hash class characteristics only. No hostname, serial, MAC, UUID or keys.
    stable := struct { Arch, Model, Processor, GPU string; MemoryBytes int64 }{
        f.Arch,f.Model,f.Processor,f.GPU,f.MemoryBytes}
    raw,_ := json.Marshal(stable)
    sum := sha256.Sum256(raw)
    p.Fingerprint = hex.EncodeToString(sum[:])
    return p
}

func output(ctx context.Context, name string, args ...string) string {
    ctx,cancel := context.WithTimeout(ctx,3*time.Second)
    defer cancel()
    b,err := exec.CommandContext(ctx,name,args...).Output()
    if err != nil { return "" }
    return strings.TrimSpace(string(b))
}

func read(path string) string {
    b,_ := os.ReadFile(path)
    return strings.TrimSpace(strings.ReplaceAll(string(b),"\x00"," "))
}

func meminfo(text, key string) int64 {
    for _,line := range strings.Split(text,"\n") {
        fields:=strings.Fields(line)
        if len(fields)>=2 && fields[0]==key+":" {
            n,_:=strconv.ParseInt(fields[1],10,64)
            if n>0 && n<1<<50 { return n*1024 }
        }
    }
    return 0
}

func Detect(ctx context.Context) Profile {
    f:=Facts{Arch:runtime.GOARCH}
    f.Model=read("/proc/device-tree/model")
    if f.Model=="" { f.Model=read("/sys/class/dmi/id/sys_vendor")+" "+read("/sys/class/dmi/id/product_name") }
    f.Model=strings.TrimSpace(f.Model)
    f.Tegra=read("/etc/nv_tegra_release")!="" && strings.Contains(strings.ToLower(read("/proc/device-tree/compatible")),"tegra")
    for _,line:=range strings.Split(read("/proc/cpuinfo"),"\n") {
        if strings.HasPrefix(line,"model name") || strings.HasPrefix(line,"Hardware") {
            if _,v,ok:=strings.Cut(line,":");ok { f.Processor=strings.TrimSpace(v);break }
        }
    }
    memory:=read("/proc/meminfo")
    f.MemoryBytes=meminfo(memory,"MemTotal")
    f.AvailableBytes=meminfo(memory,"MemAvailable")
    gpu:=output(ctx,"nvidia-smi","--query-gpu=name,memory.free","--format=csv,noheader,nounits")
    if gpu!="" {
        f.CUDA=true
        // Use a conservative single-device budget. Do not sum VRAM or
        // assume llama-bench will select whichever GPU currently has most free.
        f.GPU, f.AcceleratorFreeBytes = nvidiaMemory(gpu)
    }
    if f.Tegra && output(ctx,"nvcc","--version")!="" { f.CUDA=true;f.GPU="Tegra integrated GPU" }
    if !f.CUDA && hasROCmGPU(output(ctx,"rocminfo")) { f.HIP=true;f.GPU="ROCm GPU";f.AcceleratorFreeBytes=amdMemory() }
    if !f.CUDA && !f.HIP && output(ctx,"vulkaninfo","--summary")!="" { f.Vulkan=true }
    if runtime.GOOS=="darwin" {
        f.Model=output(ctx,"sysctl","-n","hw.model")
        f.Processor=output(ctx,"sysctl","-n","machdep.cpu.brand_string")
        f.MemoryBytes,_=strconv.ParseInt(output(ctx,"sysctl","-n","hw.memsize"),10,64)
        f.Metal=runtime.GOARCH=="arm64"
        f.AvailableBytes=appleAvailable(output(ctx,"vm_stat"))
    }
    return Recommend(f)
}

func amdMemory() int64 {
    paths,_:=filepath.Glob("/sys/class/drm/card*/device/mem_info_vram_total")
    var minimum int64
    seen:=false
    for _,path:=range paths {
        dir:=filepath.Dir(path)
        if read(filepath.Join(dir,"vendor"))!="0x1002" {continue}
        total,err:=strconv.ParseInt(read(path),10,64)
        if err!=nil || total<=0 {return 0}
        used,err:=strconv.ParseInt(read(filepath.Join(dir,"mem_info_vram_used")),10,64)
        if err!=nil || used<0 || used>=total {return 0}
        free:=total-used
        if !seen || free<minimum {minimum=free}
        seen=true
    }
    return minimum
}

func appleAvailable(text string) int64 {
    lines:=strings.Split(text,"\n")
    if len(lines)==0 {return 0}
    var pageSize int64
    for i,field:=range strings.Fields(lines[0]) {
        if field=="of" {
            fields:=strings.Fields(lines[0])
            if i+1<len(fields) {pageSize,_=strconv.ParseInt(fields[i+1],10,64)}
        }
    }
    if pageSize!=4096 && pageSize!=16384 {return 0}
    var pages int64
    for _,line:=range lines[1:] {
        key,value,ok:=strings.Cut(line,":")
        if !ok || (key!="Pages free" && key!="Pages inactive") {continue}
        n,err:=strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(value),"."),10,64)
        if err!=nil || n<0 || n>1<<40 {return 0}
        pages+=n
    }
    return pages*pageSize
}

func nvidiaMemory(text string) (string, int64) {
    var names []string
    var minimum int64
    for i,line := range strings.Split(strings.TrimSpace(text),"\n") {
        fields := strings.Split(line,",")
        if len(fields)!=2 { return strings.Join(names,", "),0 }
        names = append(names,strings.TrimSpace(fields[0]))
        n,err := strconv.ParseInt(strings.TrimSpace(fields[1]),10,64)
        if err!=nil || n<=0 || n>=1<<40 { return strings.Join(names,", "),0 }
        bytes := n*(1<<20)
        if i==0 || bytes<minimum { minimum=bytes }
    }
    return strings.Join(names,", "),minimum
}

func hasROCmGPU(text string) bool {
    for _,line := range strings.Split(text,"\n") {
        fields:=strings.Fields(line)
        if len(fields)==2 && fields[0]=="Name:" && strings.HasPrefix(fields[1],"gfx") {
            return true
        }
    }
    return false
}

// Benchmark executes an installed llama-bench against an explicit local model.
// It never downloads software/models or grants certification.
func Benchmark(ctx context.Context,p Profile,model string) error {
    info,err:=os.Stat(model)
    if err!=nil || !info.Mode().IsRegular() { return fmt.Errorf("a local regular GGUF file is required") }
    if info.Size()<=0 || info.Size()>p.ModelBudgetBytes { return fmt.Errorf("model does not fit the measured preflight memory budget") }
    file,err:=os.Open(model)
    if err!=nil { return fmt.Errorf("cannot read model: %w",err) }
    var magic [4]byte
    _,readErr:=io.ReadFull(file,magic[:])
    _ = file.Close()
    if readErr!=nil || string(magic[:])!="GGUF" { return fmt.Errorf("model is not a GGUF file") }
    binary,err:=exec.LookPath("llama-bench")
    if err!=nil { return fmt.Errorf("install llama-bench with the recommended backend first") }
    args:=append([]string{"-m",model},p.BenchmarkArgs...)
    cmd:=exec.CommandContext(ctx,binary,args...)
    cmd.Stdout=os.Stdout;cmd.Stderr=os.Stderr
    return cmd.Run()
}

// ReportedMemoryTolerance is only for comparing hardware observations across
// boots. Never add it to available memory or use it for admission/allocation.
const ReportedMemoryTolerance int64 = 1 << 30

func SameReportedMemory(a, b int64) bool {
    if a <= 0 || b <= 0 { return false }
    if a < b { a, b = b, a }
    return a-b <= ReportedMemoryTolerance
}

// MatchesReportedHardware retains the original fingerprint format and compares
// each observation against the approved baseline, not the previous observation.
// Normalize only reported total RAM after checking its bounded difference.
func MatchesReportedHardware(approved, current Profile) bool {
    if approved.Backend != current.Backend { return false }
    if approved.Fingerprint != "" && approved.Fingerprint == current.Fingerprint { return true }
    if !SameReportedMemory(approved.Facts.MemoryBytes, current.Facts.MemoryBytes) { return false }
    if Recommend(approved.Facts).Fingerprint != approved.Fingerprint ||
       Recommend(current.Facts).Fingerprint != current.Fingerprint { return false }
    normalized := current.Facts
    normalized.MemoryBytes = approved.Facts.MemoryBytes
    return Recommend(normalized).Fingerprint == approved.Fingerprint
}
