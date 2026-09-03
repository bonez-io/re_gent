import { Route, Routes } from 'react-router-dom'
import { ConnectScreen } from './ConnectScreen'
import { DoneScreen } from './DoneScreen'
import { OnboardingIndex } from './OnboardingIndex'
import { UsersScreen } from './UsersScreen'

/**
 * Mounted at "setup/*" (self-hosted) or "o/:slug/setup/*" (managed). The index route
 * resumes at whichever screen matches the org's current onboarding state.
 */
export function OnboardingRoutes() {
  return <Routes>
    <Route index element={<OnboardingIndex />} />
    <Route path="connect" element={<ConnectScreen />} />
    <Route path="users" element={<UsersScreen />} />
    <Route path="done" element={<DoneScreen />} />
  </Routes>
}
