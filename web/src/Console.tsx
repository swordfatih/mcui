import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import axios from 'axios'

type Server = {
  name: string
  edition: 'bedrock' | 'java'
  status: string
}

type CommandResult = { output: string }

const api = axios.create({ baseURL: '/api' })
const errorMessage = (error: unknown) => axios.isAxiosError(error)
  ? (error.response?.data?.error ?? error.message)
  : String(error)

export default function Console({ server }: { server: Server }) {
  const key = encodeURIComponent(server.name)
  const queryClient = useQueryClient()
  const outputRef = useRef<HTMLPreElement>(null)
  const [command, setCommand] = useState('')
  const [lastCommand, setLastCommand] = useState('')
  const [followLogs, setFollowLogs] = useState(true)
  const logs = useQuery({
    queryKey: ['logs', key],
    queryFn: async () => (await api.get<{ logs: string }>(`/server-details/${key}/logs`)).data,
    refetchInterval: 3000,
  })
  const sendCommand = useMutation({
    mutationFn: async (value: string) => (await api.post<CommandResult>(`/server-details/${key}/command`, { command: value })).data,
    onSuccess: async (_, value) => {
      setLastCommand(value)
      setCommand('')
      setFollowLogs(true)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['logs', key] }),
        queryClient.invalidateQueries({ queryKey: ['servers'] }),
      ])
    },
  })

  useEffect(() => {
    if (followLogs && outputRef.current) {
      outputRef.current.scrollTop = outputRef.current.scrollHeight
    }
  }, [logs.data?.logs, followLogs])

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const value = command.trim()
    if (value && server.status === 'running' && !sendCommand.isPending) {
      sendCommand.mutate(value)
    }
  }

  return <div className="console-page">
    <div className="infra-intro">
      <div>
        <p className="kicker">CONSOLE</p>
        <h2>Commands & live output</h2>
        <p className="muted">Run a server command and watch the latest activity.</p>
      </div>
      <span className={`console-connection ${server.status === 'running' ? 'online' : ''}`}>
        <i />{server.status === 'running' ? 'Server online' : 'Server offline'}
      </span>
    </div>

    <section className="infra-section console-command-section">
      <div className="infra-section-head">
        <span className="infra-step">01</span>
        <div>
          <h3>{server.edition === 'java' ? 'Java RCON' : 'Bedrock command'}</h3>
          <p>{server.edition === 'java'
            ? 'Commands run through the server’s built-in RCON client.'
            : 'Commands go to the Bedrock server console. Replies appear in the live output below.'}</p>
        </div>
      </div>
      <form className="console-command-form" onSubmit={submit}>
        <label htmlFor="server-command">Command</label>
        <div className="console-command-row">
          <span aria-hidden="true" className="console-prompt">›</span>
          <input
            id="server-command"
            autoComplete="off"
            spellCheck={false}
            maxLength={1024}
            value={command}
            onChange={event => setCommand(event.target.value)}
            placeholder={server.edition === 'java' ? 'say Hello, world!' : 'gamerule showcoordinates true'}
            disabled={server.status !== 'running' || sendCommand.isPending}
          />
          <button className="primary" type="submit" disabled={server.status !== 'running' || !command.trim() || sendCommand.isPending}>
            {sendCommand.isPending ? 'Sending…' : 'Run command'}
          </button>
        </div>
        <p className="console-help">{server.status === 'running'
          ? 'Press Enter to send. A leading / is optional.'
          : 'Start the server to send commands.'}</p>
      </form>
      {sendCommand.isError && <p className="error console-feedback" role="alert">{errorMessage(sendCommand.error)}</p>}
      {sendCommand.isSuccess && <div className="console-result" role="status">
        <span className="console-result-label">{server.edition === 'java' ? `RCON response · ${lastCommand}` : `Sent · ${lastCommand}`}</span>
        {server.edition === 'java'
          ? <pre>{sendCommand.data.output || 'Command completed without a response.'}</pre>
          : <p>Check the live output for the server’s response.</p>}
      </div>}
    </section>

    <section className="infra-section console-log-section">
      <div className="infra-section-head console-log-head">
        <span className="infra-step">02</span>
        <div>
          <h3>Live output</h3>
          <p>Last 200 lines from Docker Compose · refreshes every three seconds</p>
        </div>
        <button className="console-refresh" type="button" onClick={() => logs.refetch()} disabled={logs.isFetching}>
          {logs.isFetching ? 'Refreshing…' : 'Refresh now'}
        </button>
      </div>
      <div className="console-terminal">
        <div className="console-terminal-bar">
          <span className="console-terminal-dots" aria-hidden="true"><i /><i /><i /></span>
          <span>{server.name} / output</span>
          <span className="console-live"><i />LIVE</span>
        </div>
        {logs.isError
          ? <p className="error console-log-error" role="alert">{errorMessage(logs.error)}</p>
          : <pre
            ref={outputRef}
            className="console-output"
            onScroll={event => {
              const element = event.currentTarget
              setFollowLogs(element.scrollHeight - element.scrollTop - element.clientHeight < 48)
            }}
          >{logs.data?.logs || (logs.isLoading ? 'Loading server output…' : 'No log output yet.')}</pre>}
      </div>
      {!followLogs && <button className="console-follow" type="button" onClick={() => setFollowLogs(true)}>Jump to latest ↓</button>}
    </section>
  </div>
}
