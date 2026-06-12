const userAgent = (process.env.npm_config_user_agent || "").toLowerCase();
const execPath = (process.env.npm_execpath || "").toLowerCase();
const isBunRuntime = Boolean(process.versions?.bun);
const isBunLifecycle = userAgent === "" || userAgent.startsWith("bun/");
const invokedByOtherPackageManager =
  userAgent.startsWith("npm/") ||
  userAgent.startsWith("pnpm/") ||
  userAgent.startsWith("yarn/") ||
  execPath.includes("npm-cli") ||
  execPath.includes("pnpm") ||
  execPath.includes("yarn");

if (!isBunRuntime || !isBunLifecycle || invokedByOtherPackageManager) {
  console.error("RouterX frontend only supports Bun.");
  console.error("Use: bun install");
  console.error("Then: bun run dev");
  process.exit(1);
}
