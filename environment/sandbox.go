package environment

import (
	"fmt"
	"strings"
)

// baseSandboxPolicy is derived from two Apache 2.0 / MIT licensed references:
//   - anthropic-experimental/sandbox-runtime (Apache 2.0) — primary reference
//   - openai/codex macos-seatbelt.ts (MIT) — sysctl allowlist
const baseSandboxPolicy = `(version 1)

(deny default (with message "cham sandbox: operation not permitted"))

; read-only access to the whole filesystem
(allow file-read*)

; child processes inherit this policy
(allow process-exec)
(allow process-fork)
(allow signal (target self))
(allow signal (target children))

; /dev/null writes only
(allow file-write-data
  (require-all
    (path "/dev/null")
    (vnode-type CHARACTER-DEVICE)))

; device access
(allow file-write* (path-prefix "/dev/tty"))  ; TTY control (tcsetattr/setRawMode)
(allow file-ioctl  (path-prefix "/dev/tty"))
(allow file-write* (path "/dev/dtracehelper")) ; DTrace probes baked into Node.js
(allow file-write* (path "/dev/autofs_nowait")) ; automounter check

; mach services — missing entries cause hangs (blocked mach_msg never returns)
(allow mach-lookup
  (global-name "com.apple.logd")
  (global-name "com.apple.system.logger")
  (global-name "com.apple.system.opendirectoryd.api")
  (global-name "com.apple.system.opendirectoryd.membership") ; ACL membership checks on file ops
  (global-name "com.apple.bsd.dirhelper")                   ; per-user temp-dir setup (confstr)
  (global-name "com.apple.cfprefsd.daemon")
  (global-name "com.apple.cfprefsd.agent")
  (global-name "com.apple.SecurityServer"))   ; keychain access

; hardware + kernel info sysctls — allow all reads; restriction per-name is fragile
; (agents crash at startup when they read a sysctl not in the allowlist) and
; sysctl-read is read-only system info with no meaningful security boundary.
(allow sysctl-read)`

const networkSandboxPolicy = `
; network access
(allow network*)
(allow system-socket)
(allow mach-lookup
  (global-name "com.apple.mDNSResponder")
  (global-name "com.apple.mDNSResponderHelper"))`

// SandboxExecSetup builds sandbox-exec arguments for an agent run.
type SandboxExecSetup struct {
	WritablePaths  []string
	DenyWithin     []string // paths denied for writes (subpath match; e.g. .git/hooks)
	DenyRead       []string // exact file paths denied for reading (literal match)
	DenyReadDirs   []string // directory subtrees denied for reading (subpath match)
	AllowNetwork   bool
	ApprovalSocket string
}

// Args returns the sandbox-exec arguments (policy, -D params) without the command.
func (s *SandboxExecSetup) Args() []string {
	profile := BuildSandboxProfile(s.WritablePaths, s.DenyWithin, s.DenyRead, s.DenyReadDirs, s.AllowNetwork, s.ApprovalSocket)
	args := []string{"-p", profile}
	for i, p := range s.WritablePaths {
		args = append(args, "-D", fmt.Sprintf("WRITABLE_ROOT_%d=%s", i, p))
	}
	for i, p := range s.DenyWithin {
		args = append(args, "-D", fmt.Sprintf("DENY_WITHIN_%d=%s", i, p))
	}
	for i, p := range s.DenyRead {
		args = append(args, "-D", fmt.Sprintf("DENY_READ_%d=%s", i, p))
	}
	for i, p := range s.DenyReadDirs {
		args = append(args, "-D", fmt.Sprintf("DENY_READ_DIR_%d=%s", i, p))
	}
	if s.ApprovalSocket != "" {
		args = append(args, "-D", "APPROVAL_SOCKET="+s.ApprovalSocket)
	}
	return args
}

// BuildSandboxProfile builds a macOS sandbox-exec policy string.
func BuildSandboxProfile(writablePaths, denyWithin, denyRead, denyReadDirs []string, allowNetwork bool, approvalSocket string) string {
	var sb strings.Builder
	sb.WriteString(baseSandboxPolicy)

	if len(writablePaths) > 0 {
		sb.WriteString("\n(allow file-write*\n")
		for i := range writablePaths {
			fmt.Fprintf(&sb, "  (subpath (param \"WRITABLE_ROOT_%d\"))\n", i)
		}
		sb.WriteString(")\n")
	}

	for i := range denyWithin {
		fmt.Fprintf(&sb, "\n(deny file-write* (subpath (param \"DENY_WITHIN_%d\")))\n", i)
		fmt.Fprintf(&sb, "(deny file-write-unlink (subpath (param \"DENY_WITHIN_%d\")))\n", i)
		fmt.Fprintf(&sb, "(deny file-write-create (subpath (param \"DENY_WITHIN_%d\")))\n", i)
	}

	for i := range denyRead {
		fmt.Fprintf(&sb, "\n(deny file-read* (literal (param \"DENY_READ_%d\")))\n", i)
	}

	for i := range denyReadDirs {
		fmt.Fprintf(&sb, "\n(deny file-read* (subpath (param \"DENY_READ_DIR_%d\")))\n", i)
	}

	if allowNetwork {
		sb.WriteString(networkSandboxPolicy)
	}

	if approvalSocket != "" {
		sb.WriteString("\n(allow file-write*\n")
		sb.WriteString("  (literal (param \"APPROVAL_SOCKET\")))\n")
	}

	return sb.String()
}
