import { useState } from 'react'
import { Chat, openExternal } from '../bindings'
import type { ChatReply } from '../types'
import { ms } from '../format'
import { AddKey } from './Providers'

const SAMPLE = 'In two short sentences, what kinds of everyday tasks can you help me with?'

type Step = 'choose' | 'key' | 'local' | 'try'

/**
 * What the app shows instead of Chat while nothing can answer: connect a
 * model, then ask one sample question, so the first answer arrives inside
 * the setup rather than after it. Every step works without a terminal.
 */
export function FirstRun({
  onRecheck,
  onDone,
}: {
  /** Look for models again; resolves to whether any can answer now. */
  onRecheck: () => Promise<boolean>
  onDone: () => void
}) {
  const [step, setStep] = useState<Step>('choose')
  const [checking, setChecking] = useState(false)
  const [notFound, setNotFound] = useState(false)
  const [sending, setSending] = useState(false)
  const [reply, setReply] = useState<ChatReply | null>(null)

  const recheck = async () => {
    setChecking(true)
    setNotFound(false)
    try {
      if (await onRecheck()) setStep('try')
      else setNotFound(true)
    } finally {
      setChecking(false)
    }
  }

  const send = async () => {
    setSending(true)
    setReply(null)
    try {
      setReply(await Chat(SAMPLE, '', '', '', ''))
    } catch (e) {
      setReply({ error: e instanceof Error ? e.message : String(e) } as ChatReply)
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="banners">
      <div className="set">
        <section className="set__group" aria-labelledby="firstrun-title">
          <h2 className="set__title" id="firstrun-title">
            {step === 'try' ? 'Try your first question' : 'Connect Hydra to an AI model'}
          </h2>

          {step === 'choose' && (
            <>
              <p className="set__note">
                Hydra doesn't come with an AI model of its own. Connect one, and Hydra will send each request to
                the model best suited to it. You can add more later in Settings.
              </p>
              <div className="set__rows">
                <div className="set__row">
                  <span className="set__label">
                    Use an online AI service
                    <span className="set__detail">
                      Paste a key from OpenAI, Anthropic, Google or another provider. The provider charges your
                      account for what you use, usually a fraction of a cent per question.
                    </span>
                  </span>
                  <button className="set__save" onClick={() => setStep('key')}>
                    Add a key
                  </button>
                </div>
                <div className="set__row">
                  <span className="set__label">
                    Use a free model on this computer
                    <span className="set__detail">
                      Install a free app that runs AI models on your own computer. Nothing leaves your machine, but
                      it needs a recent computer and answers more slowly.
                    </span>
                  </span>
                  <button className="pagehead__btn" onClick={() => setStep('local')}>
                    Set up a local model
                  </button>
                </div>
              </div>
            </>
          )}

          {step === 'key' && (
            <>
              <p className="set__note">An API key is like a password that lets Hydra use your account with an AI provider.</p>
              <AddKey onWorking={() => void recheck()} />
            </>
          )}

          {step === 'local' && (
            <>
              <p className="set__note">Hydra finds a local model app on its own once it is running.</p>
              <div className="set__rows">
                <div className="set__row">
                  <span className="set__label">
                    1. Download Ollama, a free app, and open it
                    <span className="set__detail">Available for Mac, Windows and Linux.</span>
                  </span>
                  <button className="pagehead__btn" onClick={() => openExternal('https://ollama.com/download')}>
                    Open ollama.com
                  </button>
                </div>
                <div className="set__row">
                  <span className="set__label">
                    2. In Ollama, choose a model and send it any message
                    <span className="set__detail">
                      That downloads the model, which can take a few minutes. A small one such as Llama 3.2 or
                      Qwen 3 is a good start.
                    </span>
                  </span>
                </div>
                <div className="set__row">
                  <span className="set__label">
                    3. Come back here
                    <span className="set__detail">
                      Already use LM Studio or another local model app? Start its server, then check again.
                    </span>
                  </span>
                  <button className="set__save" onClick={() => void recheck()} disabled={checking}>
                    {checking ? 'Looking…' : 'Check again'}
                  </button>
                </div>
              </div>
            </>
          )}

          {notFound && step !== 'try' && (
            <p className="setup__error" role="alert">
              {step === 'local'
                ? 'No model found yet. Make sure the app is open and has finished downloading a model, then check again.'
                : 'The key works, but Hydra could not find a model to use with it yet. Wait a moment, then try again.'}
            </p>
          )}

          {(step === 'key' || step === 'local') && (
            <p className="set__note">
              <button className="pagehead__btn" onClick={() => setStep('choose')}>
                Back
              </button>
            </p>
          )}

          {step === 'try' && (
            <>
              <p className="set__note">Hydra is connected. Send a sample question to see it work.</p>
              <div className="set__rows">
                <div className="set__row">
                  <span className="set__label">
                    “{SAMPLE}”
                    {reply?.output && <span className="set__detail" role="status">{reply.output}</span>}
                    {reply?.output && (
                      <span className="set__detail">
                        Answered by {reply.model || reply.head} in {ms(reply.durationMs)}.
                      </span>
                    )}
                    {reply?.error && (
                      <span className="setup__error" role="alert">
                        {reply.error}
                      </span>
                    )}
                  </span>
                  {!reply?.output && (
                    <button className="set__save" onClick={() => void send()} disabled={sending}>
                      {sending ? 'Asking…' : 'Send'}
                    </button>
                  )}
                </div>
              </div>
              {reply?.output && (
                <button className="empty__cta" onClick={onDone}>
                  Start using Hydra
                </button>
              )}
            </>
          )}
          {/* Never a trap: someone who already has a model, or wants to look
              around first, can leave and come back from Chat. */}
          {!reply?.output && (
            <p className="set__note">
              <button className="pagehead__btn" onClick={onDone}>
                Skip for now
              </button>
            </p>
          )}
        </section>
      </div>
    </div>
  )
}
