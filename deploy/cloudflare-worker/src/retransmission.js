export function normalizeRetransmission(value) {
  if (value == null) return null;
  if (typeof value !== "object" || Array.isArray(value) ||
      !Number.isSafeInteger(value.sent_bytes) || value.sent_bytes <= 0 ||
      !Number.isSafeInteger(value.retransmitted_bytes) || value.retransmitted_bytes < 0 ||
      value.retransmitted_bytes > value.sent_bytes) return undefined;
  return { sent_bytes: value.sent_bytes, retransmitted_bytes: value.retransmitted_bytes };
}

export function retransmissionText(value) {
  const stats = normalizeRetransmission(value);
  if (!stats) return "—";
  const percent = stats.retransmitted_bytes / stats.sent_bytes * 100;
  return stats.retransmitted_bytes > 0 && percent < 0.01 ? "<0.01%" : `${percent.toFixed(2)}%`;
}

export function retransmissionAnsiCode(value) {
  const stats = normalizeRetransmission(value);
  if (!stats) return "38;2;153;169;189";
  const percent = stats.retransmitted_bytes / stats.sent_bytes * 100;
  if (percent < 1) return "32";
  if (percent < 5) return "38;2;255;165;0";
  return "31";
}
