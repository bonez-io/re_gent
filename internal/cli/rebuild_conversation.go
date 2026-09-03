package cli

import "github.com/bonez-io/re_gent/internal/index"

// storedConversationEntry is the portable conversation shape attached to a
// step; the reconstruction itself now lives in index.RebuildConversation so
// a server can mirror pushed steps the way `rgt pull` restores them here.
type storedConversationEntry = index.ConversationEntry
