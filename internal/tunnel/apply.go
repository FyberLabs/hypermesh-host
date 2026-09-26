// Package tunnel applies a WireGuard (or equivalent) session allocated
// by the control plane. One peer per host, not per lease. No custom overlay.
package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)
