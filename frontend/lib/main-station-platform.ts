export function normalizeMainStationPlatform(value?: string) {
  const normalized = value?.trim().toLowerCase() ?? ""
  if (normalized === "claude") return "anthropic"
  if (normalized === "google") return "gemini"
  if (normalized === "xai") return "grok"
  return normalized
}

export function mainStationHealthAPIMode(platform?: string) {
  switch (normalizeMainStationPlatform(platform)) {
    case "anthropic":
      return "anthropic"
    case "gemini":
      return "gemini"
    case "image":
      return "openai_image"
    default:
      return "openai_chat"
  }
}

export function mainStationPlatformsMatch(left?: string, right?: string) {
  const normalizedLeft = normalizeMainStationPlatform(left)
  const normalizedRight = normalizeMainStationPlatform(right)
  return normalizedLeft !== "" && normalizedLeft === normalizedRight
}
