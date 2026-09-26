// Chameleon permission gate for Pi agent — loaded via --extension flag.
// In headless mode (via --mode json) this auto-blocks dangerous commands.
// In interactive mode it prompts the user.
const BLOCKED = [
  /rm\s+-rf\s+\//,
  /sudo\s+rm/,
  /chmod\s+777/,
  /mkfs\./,
  /dd\s+if=/,
  />\s*\/dev\/sd/,
  /:\(\)\s*\{\s*:\|:&\s*\};:/,
  /git\s+push\s+--force.*origin\s+(main|master)/,
]

export default function() {
  return {
    name: "cham-permission-gate",
    async beforeTool(_, tool) {
      if (tool.name !== "bash") return
      const cmd = tool.arguments?.command ?? ""
      if (BLOCKED.some((r) => r.test(cmd))) {
        throw new Error(`Blocked dangerous command: ${cmd}`)
      }
    },
  }
}
