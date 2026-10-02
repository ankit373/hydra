import type { ReactNode } from 'react'

interface Props {
  title: string
  subtitle?: string
  /** Where the numbers below came from and as of when. Rendered apart from the
   *  subtitle because "what this is" and "what evidence it rests on" are
   *  different claims, and the second is the one Hydra keeps getting asked. */
  provenance?: string
  actions?: ReactNode
  /** A toolbar row under the title: search, filters, sort, view switchers. */
  toolbar?: ReactNode
}

/** Every view's header. Before this, each view hand-rolled its own h1. */
export function PageHeader({ title, subtitle, provenance, actions, toolbar }: Props) {
  return (
    <div className="pagehead">
      <div className="pagehead__row">
        <div className="pagehead__titles">
          <h1 className="pagehead__title">{title}</h1>
          {subtitle && <p className="pagehead__sub">{subtitle}</p>}
        </div>
        {actions && <div className="pagehead__actions">{actions}</div>}
      </div>
      {provenance && <p className="pagehead__prov">{provenance}</p>}
      {toolbar && <div className="pagehead__toolbar">{toolbar}</div>}
    </div>
  )
}
