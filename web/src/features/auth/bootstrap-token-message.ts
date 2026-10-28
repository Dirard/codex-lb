// First-run setup always needs a bootstrap token; the message names where the
// operator finds it (server environment or the generated one-time file).
export function bootstrapTokenMessageKey(source: "environment" | "file" | null): string {
  switch (source) {
    case "environment":
      return "auth.bootstrap.tokenConfigured";
    case "file":
      return "auth.bootstrap.tokenFile";
    default:
      return "auth.bootstrap.tokenMissing";
  }
}
