import { expect, test } from 'claude-code/testing'

import { liveViewFrom, releaseBinary, unixPlatform, windowsPlatform } from '../hooks/bridge'

test('uname names the platforms a bridge is built for', () => {
  expect(unixPlatform('Darwin arm64\n')).toEqual({ goos: 'darwin', goarch: 'arm64' })
  expect(unixPlatform('Darwin x86_64')).toEqual({ goos: 'darwin', goarch: 'amd64' })
  expect(unixPlatform('Linux x86_64')).toEqual({ goos: 'linux', goarch: 'amd64' })
  expect(unixPlatform('Linux aarch64')).toEqual({ goos: 'linux', goarch: 'arm64' })
  expect(unixPlatform('FreeBSD amd64')).toBe(null)
  expect(unixPlatform('Linux i686')).toBe(null)
  expect(unixPlatform('')).toBe(null)
})

test('PROCESSOR_ARCHITECTURE names the Windows platforms a bridge is built for', () => {
  expect(windowsPlatform('AMD64')).toEqual({ goos: 'windows', goarch: 'amd64' })
  expect(windowsPlatform('ARM64')).toEqual({ goos: 'windows', goarch: 'arm64' })
  expect(windowsPlatform('x86')).toBe(null)
  expect(windowsPlatform(undefined)).toBe(null)
})

test('a release keeps one binary per platform under bin/', () => {
  expect(releaseBinary('/p', { goos: 'linux', goarch: 'arm64' })).toBe('/p/bin/android-bridge-linux-arm64')
  expect(releaseBinary('C:/p', { goos: 'windows', goarch: 'amd64' })).toBe('C:/p/bin/android-bridge-windows-amd64.exe')
})

test('only the announcement of a loopback live view counts', () => {
  expect(liveViewFrom('{"liveView":"http://127.0.0.1:52833"}')).toBe('http://127.0.0.1:52833')
  expect(liveViewFrom('{"liveView":"http://example.com:80"}')).toBe(null)
  expect(liveViewFrom('{"liveView":"http://127.0.0.1:52833/x"}')).toBe(null)
  expect(liveViewFrom('{"url":"http://127.0.0.1:52833"}')).toBe(null)
  expect(liveViewFrom('time=1 level=INFO msg="live view ready"')).toBe(null)
  expect(liveViewFrom('null')).toBe(null)
})
