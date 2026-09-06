export function transcriptImageSource(
  src: string,
  origin: string,
  token: string,
) {
  if (/^data:image\/[a-z0-9.+-]+;base64,/i.test(src)) return { uri: src };
  if (!src) throw new Error("Image source is unavailable.");
  const url = new URL(src, origin);
  if (
    !["https:", "http:"].includes(url.protocol) ||
    url.username ||
    url.password
  )
    throw new Error("Image source is unavailable.");
  return {
    uri: url.href,
    ...(url.origin === new URL(origin).origin && token
      ? { headers: { Authorization: `Bearer ${token}` } }
      : {}),
  };
}
