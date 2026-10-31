/**
 * The label on a graph node, cut to fit without losing what distinguishes it.
 *
 * One implementation for both graphs. There were two, disagreeing about which
 * half to keep: SessionGraph cut the front and RunGraph cut the back, so one
 * lost the provider and the other lost the model, and every Anthropic model
 * rendered as `anthropic/c…` (#1120).
 *
 * The tail is what distinguishes a model id (`claude-sonnet-4.5`) and a path
 * (`token.go`) alike, so the cut keeps the tail. It lands on a `/`, never
 * inside a segment: a blind character count produced `…ropic/` and
 * `…thropic/`, which made two models of one provider look like two providers.
 */
export function graphLabel(s: string, max: number): string {
  if (s.length <= max) return s

  // Drop whole leading segments while what remains still fits behind the
  // elision. `…/` costs 2, which is why this is not a plain slice.
  const parts = s.split('/')
  for (let i = 1; i < parts.length; i++) {
    const tail = parts.slice(i).join('/')
    if (tail.length + 2 <= max) return `…/${tail}`
  }

  // Even the last segment does not fit behind an elision. Its start is what
  // separates `claude-sonnet` from `claude-opus`, so keep that rather than
  // eliding the one part that still carries information.
  const last = parts[parts.length - 1]
  return last.length <= max ? last : `${last.slice(0, max - 1)}…`
}
