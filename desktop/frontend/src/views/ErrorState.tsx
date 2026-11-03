import { PageHeader } from './PageHeader'

interface Props {
  /** The view, so a failed read still says where you are. */
  title: string
  /** The thing that could not be read, named: "the spend log", not "data". */
  what: string
  detail: string
  onRetry: () => void
}

/**
 * What a view renders when its read failed. The old `.error` was the raw Go
 * string alone in a red monospace box, with the page header gone: it named
 * neither the view nor the read, and offered nothing to do about it (#1100).
 */
export function ErrorState({ title, what, detail, onRetry }: Props) {
  return (
    <>
      <PageHeader title={title} />
      <div className="failure" role="alert">
        <p className="failure__title">Couldn't read {what}</p>
        <pre className="failure__detail">{detail}</pre>
        <button className="failure__retry" onClick={onRetry}>
          Try again
        </button>
      </div>
    </>
  )
}
