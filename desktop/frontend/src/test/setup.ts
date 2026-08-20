import '@testing-library/jest-dom/vitest'

// jsdom has no scroll layout engine, so Element.scrollTo is simply absent —
// ChatDock's log auto-scroll (a real DOM API call, not a library) would throw
// on every render otherwise.
Element.prototype.scrollTo = () => {}
