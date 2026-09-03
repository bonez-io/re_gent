import { Route, Routes } from 'react-router-dom'
import { ConnectScreen } from './ConnectScreen'
import { DoneScreen } from './DoneScreen'
import { OnboardingIndex } from './OnboardingIndex'
import { TutorialScreen } from './TutorialScreen'
import { UsersScreen } from './UsersScreen'

/**
 * Mounted at "setup/*" (self-hosted) or "o/:slug/setup/*" (managed). The index route
 * resumes at whichever screen matches the org's current onboarding state. "tutorial" is
 * UI-only — it has no corresponding server onboarding state, so a direct or refreshed
 * visit to the bare index route never lands on it; ConnectScreen navigates there directly.
 */
export function OnboardingRoutes() {
  return <Routes>
    <Route index element={<OnboardingIndex />} />
    <Route path="connect" element={<ConnectScreen />} />
    <Route path="tutorial" element={<TutorialScreen />} />
    <Route path="users" element={<UsersScreen />} />
    <Route path="done" element={<DoneScreen />} />
  </Routes>
}
