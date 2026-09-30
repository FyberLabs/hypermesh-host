package hardware
import "testing"

func TestClassificationAndMemoryBudget(t *testing.T) {
    cases:=[]struct{ facts Facts; family,backend string }{
        {Facts{Tegra:true,Model:"NVIDIA Jetson AGX Orin",CUDA:true},"nvidia-jetson-agx","cuda"},
        {Facts{Tegra:true,Model:"Jetson Orin NX",CUDA:true},"nvidia-jetson","cuda"},
        {Facts{Model:"NVIDIA DGX Spark",CUDA:true,AcceleratorFreeBytes:16<<30},"nvidia-dgx","cuda"},
        {Facts{Model:"workstation",HIP:true,AcceleratorFreeBytes:16<<30},"generic","hip"},
        {Facts{Model:"workstation"},"generic","cpu"},
    }
    for _,c:=range cases {
        c.facts.MemoryBytes=64<<30;c.facts.AvailableBytes=32<<30
        p:=Recommend(c.facts)
        if p.Family!=c.family || p.Backend!=c.backend {t.Fatalf("%+v",p)}
        if p.ModelBudgetBytes<=0 || p.ModelBudgetBytes>=c.facts.AvailableBytes {t.Fatal("unsafe budget")}
    }
}

func TestFingerprintIsClassNotChangingMemoryAvailability(t *testing.T) {
    f:=Facts{Arch:"arm64",Model:"AGX",MemoryBytes:64<<30,AvailableBytes:20<<30}
    a:=Recommend(f);f.AvailableBytes=10<<30;b:=Recommend(f)
    if a.Fingerprint!=b.Fingerprint || a.ModelBudgetBytes==b.ModelBudgetBytes {t.Fatal("fingerprint or budget")}
    f.Model="different"
    if Recommend(f).Fingerprint==a.Fingerprint {t.Fatal("model not fingerprinted")}
}

func TestUnknownAcceleratorMemoryDoesNotInventCapacity(t *testing.T) {
    for _,f:=range []Facts{{CUDA:true},{HIP:true},{Vulkan:true},{}} {
        p:=Recommend(f)
        if p.ModelBudgetBytes!=0 {t.Fatal("invented memory")}
    }
    p:=Recommend(Facts{CUDA:true,AvailableBytes:64<<30})
    if p.ModelBudgetBytes!=0 {t.Fatal("used host RAM as discrete VRAM")}
}

func TestAcceleratorDetectionFailsClosed(t *testing.T) {
    _,budget:=nvidiaMemory("GPU A, 16000\nGPU B, 4000")
    if budget!=4000*(1<<20) {t.Fatal("used the largest GPU budget")}
    _,budget=nvidiaMemory("GPU A, 16000\nGPU B, [N/A]")
    if budget!=0 {t.Fatal("invented unknown GPU memory")}
    if hasROCmGPU("Name: AMD Ryzen CPU") {t.Fatal("CPU agent counted as GPU")}
    if !hasROCmGPU("  Name: gfx1100") {t.Fatal("missed ROCm GPU")}
}
