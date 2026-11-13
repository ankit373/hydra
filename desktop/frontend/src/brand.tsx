import { useId } from 'react'

// The converging-necks mark, ported inline from brand/svg/mark.svg (a local
// design-source workspace, deliberately untracked, see brand/render/build.mjs
// for the full asset pipeline). This is the one place that source lands in
// shipped frontend code. It is shown large and only where the app is empty or
// loading; the header carries the plain wordmark (#1435).
//
// Gradient ids are per instance: two marks on one page with the same id
// would both paint from whichever was defined first.
export function HydraMark({ className, decorative }: { className?: string; decorative?: boolean }) {
  // useId has colons or guillemets in it, which a url(#…) reference may not.
  const uid = useId().replace(/[^A-Za-z0-9_-]/g, '')
  const id = (name: string) => `hyG-${name}-${uid}`
  const url = (name: string) => `url(#${id(name)})`
  return (
    <svg
      className={className}
      viewBox="0 0 512 512"
      {...(decorative ? { 'aria-hidden': true } : { role: 'img', 'aria-label': 'Hydra' })}
    >
      <defs>
        <linearGradient id={id('center')} x1="256" y1="96" x2="256" y2="392" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#3DF5E6" />
          <stop offset="1" stopColor="#8B5CF6" />
        </linearGradient>
        <linearGradient id={id('left')} x1="150" y1="150" x2="256" y2="392" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#F062D2" />
          <stop offset="1" stopColor="#8B5CF6" />
        </linearGradient>
        <linearGradient id={id('right')} x1="362" y1="150" x2="256" y2="392" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#F062D2" />
          <stop offset="1" stopColor="#8B5CF6" />
        </linearGradient>
        <linearGradient id={id('headC')} x1="256" y1="92" x2="256" y2="150" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#8BFCF0" />
          <stop offset="1" stopColor="#2AF0E0" />
        </linearGradient>
        <linearGradient id={id('headS')} x1="0" y1="-28" x2="0" y2="16" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#FF8FE6" />
          <stop offset="1" stopColor="#E852C8" />
        </linearGradient>
        <radialGradient id={id('cortex')} cx="256" cy="388" r="30" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#EAFFFA" />
          <stop offset="0.45" stopColor="#00E6C3" />
          <stop offset="1" stopColor="#0A8F7C" />
        </radialGradient>
      </defs>

      <g fill="none" stroke="#8B5CF6" strokeWidth="3.5" opacity="0.32" strokeLinecap="round">
        <path d="M150 166 L256 120 L362 166" />
        <path d="M198 292 L256 274 L314 292" />
        <path d="M150 166 L198 292 M362 166 L314 292 M256 120 L256 274" />
      </g>

      <g fill="none" strokeWidth="15" strokeLinecap="round">
        <path d="M256 146 C256 236 256 306 256 372" stroke={url('center')} />
        <path d="M144 184 C158 280 200 344 250 374" stroke={url('left')} />
        <path d="M368 184 C354 280 312 344 262 374" stroke={url('right')} />
      </g>

      <circle cx="256" cy="274" r="8" fill="#B7A4FF" />
      <circle cx="198" cy="292" r="6" fill="#CDA6F2" />
      <circle cx="314" cy="292" r="6" fill="#CDA6F2" />

      <path d="M0 -28 L13 -8 L9 16 L-9 16 L-13 -8 Z" transform="translate(256 118) scale(1.18)" fill={url('headC')} />
      <path d="M0 -28 L13 -8 L9 16 L-9 16 L-13 -8 Z" transform="translate(150 164) rotate(-30) scale(1.02)" fill={url('headS')} />
      <path d="M0 -28 L13 -8 L9 16 L-9 16 L-13 -8 Z" transform="translate(362 164) rotate(30) scale(1.02)" fill={url('headS')} />

      <circle cx="256" cy="388" r="25" fill={url('cortex')} />
      <circle cx="256" cy="388" r="9" fill="#F2FFFC" />
    </svg>
  )
}
