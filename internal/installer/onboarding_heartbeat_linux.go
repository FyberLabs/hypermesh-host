//go:build linux

package installer

import (
 "fmt"
 "math"
 "os"
 "strconv"
 "strings"
)

// Kernel uptime includes suspend time. No wall-clock timestamp, configurable
// path, shell command or operator-provided uptime can become heartbeat evidence.
func measuredHeartbeatUptime()(int64,error) {
 raw,err:=os.ReadFile("/proc/uptime")
 if err!=nil{return 0,fmt.Errorf("cannot read kernel uptime")}
 return parseHeartbeatUptime(string(raw))
}
func parseHeartbeatUptime(raw string)(int64,error) {
 fields:=strings.Fields(raw)
 if len(fields)!=2||len(raw)>128{return 0,fmt.Errorf("invalid kernel uptime")}
 value,err:=strconv.ParseFloat(fields[0],64)
 if err!=nil||math.IsNaN(value)||math.IsInf(value,0)||value<0||value>float64(1<<31-1) {
  return 0,fmt.Errorf("kernel uptime is outside the supported range")
 }
 return int64(value),nil
}
