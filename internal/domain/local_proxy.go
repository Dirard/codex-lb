package domain

// LocalProxyKeyID is an internal accounting principal, never a usable Bearer
// credential. It lets explicitly keyless local traffic share the same ownership
// and settlement path as keyed traffic without creating a parallel proxy path.
const LocalProxyKeyID = "__local_proxy__"

// WarmupKeyID owns admin synthetic reservations, never client authentication.
const WarmupKeyID = "__admin_warmup__"

func IsInternalKey(id string) bool { return id == LocalProxyKeyID || id == WarmupKeyID }
