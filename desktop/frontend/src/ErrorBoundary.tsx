import { Component, type ErrorInfo, type ReactNode } from 'react'

// One malformed field in one panel used to unmount the whole app to a blank
// window, with the reason only in the devtools console. A security view that
// vanishes silently is worse than one that says what broke.
export class ErrorBoundary extends Component<
  { children: ReactNode; label: string },
  { error: Error | null }
> {
  state = { error: null as Error | null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`[${this.props.label}]`, error, info.componentStack)
  }

  render() {
    const { error } = this.state
    if (!error) return this.props.children
    return (
      <div className="error">
        {this.props.label} failed to render: {error.message}
        <div className="card__note">
          The rest of Hydra still works. This is a bug, please report it with the console output.
        </div>
      </div>
    )
  }
}
