package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// DefaultRefreshWindow is how close to expiry a stored OAuth credential must
// be before RefreshAccounts will spend a refresh grant on it. OAuth refresh
// tokens rotate on every grant, so rotating a perfectly fresh credential is
// not free: it consumes the stored refresh token and widens the window in
// which a crashed/racing writer could strand the account. Refresh only when
// the access token is already dead or about to be.
const DefaultRefreshWindow = time.Hour

// RefreshOutcome classifies what happened to one account slot.
type RefreshOutcome string

const (
	// OutcomeRefreshed means a refresh grant ran and new tokens were persisted.
	OutcomeRefreshed RefreshOutcome = "refreshed"
	// OutcomeFresh means the credential was still valid beyond the refresh
	// window, so no grant was spent.
	OutcomeFresh RefreshOutcome = "fresh"
	// OutcomeSkipped means the slot holds no refreshable OAuth credential
	// (an api_key or aws_iam account, or an OAuth slot with no refresh token).
	OutcomeSkipped RefreshOutcome = "skipped"
	// OutcomeFailed means the refresh was attempted and failed.
	OutcomeFailed RefreshOutcome = "failed"
)

// RefreshResult reports the outcome of refreshing a single account slot.
type RefreshResult struct {
	SlotKey string
	Label   string
	Outcome RefreshOutcome
	// Expires is the credential's expiry after the operation, in epoch
	// milliseconds. Zero means the credential carries no expiry.
	Expires int64
	// Reason explains a skip. Empty for other outcomes.
	Reason string
	Err    error
}

// ExpiresAt returns the credential expiry as a time, or the zero time when the
// credential carries no expiry.
func (r RefreshResult) ExpiresAt() time.Time {
	if r.Expires == 0 {
		return time.Time{}
	}
	return time.UnixMilli(r.Expires)
}

// RefreshAccounts refreshes every stored account slot of a base provider,
// rotating and persisting new tokens through the same locked write path fir's
// own mid-turn refresh uses (see refreshOAuthLocked). Slots are processed
// independently: a failure on one is recorded in its RefreshResult and does
// not abort the rest.
//
// window is the "about to expire" threshold; a credential expiring further out
// than that is left alone and reported as OutcomeFresh. Pass force to rotate
// regardless of expiry.
//
// Results are returned in AccountsForProvider order (default account first).
// An empty slice means the provider has no stored accounts at all.
func (s *AuthStorage) RefreshAccounts(ctx context.Context, providerID string, window time.Duration, force bool) []RefreshResult {
	// Enumerate against the file as it is now, not as this process last saw
	// it: another writer may have added, removed or rotated a slot since.
	s.reloadIfStale()
	accounts := s.AccountsForProvider(providerID)
	results := make([]RefreshResult, 0, len(accounts))
	for _, acct := range accounts {
		results = append(results, s.RefreshAccount(ctx, acct.SlotKey, window, force))
	}
	return results
}

// RefreshAccount refreshes a single account slot ("anthropic" or
// "anthropic#<id>"). See RefreshAccounts for the window/force semantics.
func (s *AuthStorage) RefreshAccount(ctx context.Context, slotKey string, window time.Duration, force bool) RefreshResult {
	res := RefreshResult{SlotKey: slotKey}
	cred := s.Get(slotKey)
	if cred == nil {
		res.Outcome = OutcomeFailed
		res.Err = fmt.Errorf("no stored credential for slot %q", slotKey)
		return res
	}
	_, acctID := SplitSlot(slotKey)
	res.Label = Account{Label: cred.Label, AccountID: acctID, SlotKey: slotKey}.DisplayName()
	res.Expires = cred.Expires

	if cred.Type != CredentialTypeOAuth {
		res.Outcome = OutcomeSkipped
		res.Reason = fmt.Sprintf("not an OAuth credential (%s)", cred.Type)
		return res
	}
	if cred.Refresh == "" {
		res.Outcome = OutcomeSkipped
		res.Reason = "no refresh token stored"
		return res
	}
	provider := oauthProviderForSlot(slotKey)
	if provider == nil {
		base, _ := SplitSlot(slotKey)
		res.Outcome = OutcomeFailed
		res.Err = fmt.Errorf("OAuth provider %q is not loaded — its auth extension failed to register", base)
		return res
	}

	out, err := s.refreshOAuthLocked(ctx, slotKey, provider, func(c AuthCredential) bool {
		return force || credentialNeedsRefresh(c, window)
	})
	if err != nil {
		res.Outcome = OutcomeFailed
		res.Err = err
		return res
	}
	if !out.found {
		res.Outcome = OutcomeFailed
		res.Err = fmt.Errorf("no stored OAuth credential for slot %q", slotKey)
		return res
	}
	res.Expires = out.cred.Expires
	if out.skipped {
		res.Outcome = OutcomeFresh
	} else {
		res.Outcome = OutcomeRefreshed
	}
	return res
}

// credentialNeedsRefresh reports whether a credential is expired or within
// window of expiring. A credential with no expiry (Expires == 0) never needs a
// scheduled refresh — fir treats that as "does not expire".
func credentialNeedsRefresh(cred AuthCredential, window time.Duration) bool {
	if cred.Expires == 0 {
		return false
	}
	if window < 0 {
		window = 0
	}
	return time.Now().Add(window).UnixMilli() >= cred.Expires
}

// oauthRefreshOutcome is the internal result of a locked refresh attempt.
type oauthRefreshOutcome struct {
	apiKey  string
	cred    AuthCredential
	found   bool // the slot held an OAuth credential
	skipped bool // shouldRefresh said no; cred is the untouched credential
	wrote   bool // new tokens were persisted
}

// refreshOAuthLocked is the single place fir rotates and persists an OAuth
// credential. It takes the backend lock (an exclusive flock on auth.json, so
// the guarantee holds across processes as well as goroutines), re-reads the
// credential from disk inside the lock, asks shouldRefresh whether to spend a
// grant, performs the refresh, and writes the rotated tokens back before
// releasing the lock. Read-decide-rotate-write is therefore atomic: two
// processes can never both consume the same refresh token, and the loser of
// the race re-reads the winner's fresh credential and declines to rotate.
//
// shouldRefresh is evaluated against the credential as read UNDER the lock —
// never a stale copy — which is what makes "another process already refreshed
// it" a natural no-op rather than a special case.
//
// On return the in-memory cache is synced to what was written and the mutation
// is recorded in the audit log.
func (s *AuthStorage) refreshOAuthLocked(
	ctx context.Context,
	slotKey string,
	oauthProvider ai.OAuthProvider,
	shouldRefresh func(AuthCredential) bool,
) (oauthRefreshOutcome, error) {
	type lockedResult struct {
		outcome     oauthRefreshOutcome
		updatedData AuthStorageData
	}

	raw, err := s.storage.WithLockFallible(func(current []byte) (any, []byte, error) {
		currentData := s.parseStorageData(current)
		lr := lockedResult{updatedData: currentData}

		cred, ok := currentData[slotKey]
		if !ok || cred.Type != CredentialTypeOAuth {
			return lr, nil, nil
		}
		lr.outcome.found = true

		if !shouldRefresh(cred) {
			lr.outcome.skipped = true
			lr.outcome.cred = cred
			lr.outcome.apiKey = oauthProvider.GetAPIKey(AuthCredToOAuthCreds(&cred))
			return lr, nil, nil
		}

		newCreds, err := oauthProvider.RefreshToken(ctx, AuthCredToOAuthCreds(&cred))
		if err != nil {
			return lr, nil, fmt.Errorf("OAuth token refresh failed for %s: %w", slotKey, err)
		}

		updated := carryAccountMetadata(OAuthCredsToAuthCred(newCreds), cred)
		currentData[slotKey] = updated
		b, err := json.MarshalIndent(currentData, "", "  ")
		if err != nil {
			// Refusing to write is the only safe move: the rotated refresh
			// token is already spent upstream, but a truncated/garbage
			// auth.json would strand every other account in the file too.
			return lr, nil, fmt.Errorf("encode auth storage after refresh of %s: %w", slotKey, err)
		}
		lr.outcome.cred = updated
		lr.outcome.apiKey = oauthProvider.GetAPIKey(newCreds)
		lr.outcome.wrote = true
		lr.updatedData = currentData
		return lr, b, nil
	})

	lr, _ := raw.(lockedResult)

	if err != nil {
		s.mu.Lock()
		s.recordError(err)
		s.lastKeyErrors[slotKey] = err
		s.mu.Unlock()
		return lr.outcome, err
	}

	// Sync the in-memory view to what the lock holder saw/wrote.
	s.mu.Lock()
	if lr.updatedData != nil {
		s.data = lr.updatedData
		s.loadError = nil
	}
	if lr.outcome.wrote {
		// Adopt our own write's stamp so the next staleness check is a no-op.
		s.lastStamp = s.backendStamp()
	}
	delete(s.lastKeyErrors, slotKey)
	remain := len(s.data)
	refreshed, hadCred := s.data[slotKey]
	s.mu.Unlock()

	if lr.outcome.wrote && hadCred {
		s.audit.record(AuditActionRefresh, slotKey, &refreshed, remain)
	}
	return lr.outcome, nil
}

// carryAccountMetadata copies account identity forward across a refresh.
// RefreshToken returns only the token triple and may not re-derive the
// profile; without this the account Label and identity (Extra.accountId) would
// evaporate, which would later spawn a duplicate slot on the next re-login of
// the same account (assignSlot keys on Extra.accountId).
//
// The previous credential's Extra is read through AuthCredToOAuthCreds so the
// legacy ProjectID column is lifted in too, and the merged result is mirrored
// back out to that column — otherwise a Google account whose project id lived
// only in the legacy column would lose it on the first refresh.
func carryAccountMetadata(updated, previous AuthCredential) AuthCredential {
	if previous.Label != "" {
		updated.Label = previous.Label
	}
	prevExtra := AuthCredToOAuthCreds(&previous).Extra
	if accountIDFromCreds(AuthCredToOAuthCreds(&updated)) == "" && len(prevExtra) > 0 {
		if updated.Extra == nil {
			updated.Extra = make(map[string]any, len(prevExtra))
		}
		for k, v := range prevExtra {
			if _, exists := updated.Extra[k]; !exists {
				updated.Extra[k] = v
			}
		}
	}
	if projectID, ok := updated.Extra["projectId"].(string); ok && projectID != "" {
		updated.ProjectID = projectID
	}
	return updated
}
