package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ── Clerk Users ───────────────────────────────────────────────────────────────

type ClerkUser struct {
	ClerkUserID string
	Email       string
	FirstName   string
	LastName    string
	ImageURL    string
	SyncedAt    time.Time
}

func (s *Store) UpsertClerkUser(ctx context.Context, u *ClerkUser) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO clerk_users (clerk_user_id, email, first_name, last_name, image_url, synced_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (clerk_user_id) DO UPDATE
		   SET email = EXCLUDED.email,
		       first_name = EXCLUDED.first_name,
		       last_name = EXCLUDED.last_name,
		       image_url = EXCLUDED.image_url,
		       synced_at = now()`,
		u.ClerkUserID, u.Email, u.FirstName, u.LastName, u.ImageURL,
	)
	return err
}

func (s *Store) GetClerkUser(ctx context.Context, clerkUserID string) (*ClerkUser, error) {
	u := &ClerkUser{}
	err := s.db.QueryRowContext(ctx,
		`SELECT clerk_user_id, email, first_name, last_name, image_url, synced_at
		 FROM clerk_users WHERE clerk_user_id = $1`,
		clerkUserID,
	).Scan(&u.ClerkUserID, &u.Email, &u.FirstName, &u.LastName, &u.ImageURL, &u.SyncedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// DeleteClerkUser soft-deletes all org memberships for the user when Clerk
// fires user.deleted. We do NOT delete the clerk_users row itself to preserve
// audit trail references.
func (s *Store) DeleteClerkUser(ctx context.Context, clerkUserID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE org_members SET deleted_at = now()
		 WHERE clerk_user_id = $1 AND deleted_at IS NULL`,
		clerkUserID,
	)
	return err
}

// ── Organizations ─────────────────────────────────────────────────────────────

type Organization struct {
	ID           string
	Name         string
	Slug         string
	CreatedBy    string
	BillingEmail string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

func (s *Store) CreateOrganization(ctx context.Context, name, slug, createdBy, billingEmail string) (*Organization, error) {
	org := &Organization{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO organizations (name, slug, created_by, billing_email)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, name, slug, created_by, billing_email, created_at, updated_at, deleted_at`,
		name, slug, createdBy, billingEmail,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy, &org.BillingEmail,
		&org.CreatedAt, &org.UpdatedAt, &org.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("create organization: %w", err)
	}
	return org, nil
}

func (s *Store) GetOrganizationByID(ctx context.Context, orgID string) (*Organization, error) {
	org := &Organization{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, created_by, billing_email, created_at, updated_at, deleted_at
		 FROM organizations WHERE id = $1 AND deleted_at IS NULL`,
		orgID,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy, &org.BillingEmail,
		&org.CreatedAt, &org.UpdatedAt, &org.DeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get organization: %w", err)
	}
	return org, nil
}

func (s *Store) GetOrganizationBySlug(ctx context.Context, slug string) (*Organization, error) {
	org := &Organization{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, created_by, billing_email, created_at, updated_at, deleted_at
		 FROM organizations WHERE slug = $1 AND deleted_at IS NULL`,
		slug,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy, &org.BillingEmail,
		&org.CreatedAt, &org.UpdatedAt, &org.DeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get organization by slug: %w", err)
	}
	return org, nil
}

// GetOrganizationsForUser returns all active orgs where the user is an active member.
func (s *Store) GetOrganizationsForUser(ctx context.Context, clerkUserID string) ([]*Organization, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.id, o.name, o.slug, o.created_by, o.billing_email, o.created_at, o.updated_at, o.deleted_at
		 FROM organizations o
		 JOIN org_members m ON m.org_id = o.id
		 WHERE m.clerk_user_id = $1
		   AND m.deleted_at IS NULL
		   AND o.deleted_at IS NULL
		 ORDER BY o.created_at ASC`,
		clerkUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("list orgs for user: %w", err)
	}
	defer rows.Close()
	var orgs []*Organization
	for rows.Next() {
		o := &Organization{}
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedBy, &o.BillingEmail,
			&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

func (s *Store) UpdateOrganization(ctx context.Context, orgID, name, billingEmail string) (*Organization, error) {
	org := &Organization{}
	err := s.db.QueryRowContext(ctx,
		`UPDATE organizations
		 SET name = $1, billing_email = $2, updated_at = now()
		 WHERE id = $3 AND deleted_at IS NULL
		 RETURNING id, name, slug, created_by, billing_email, created_at, updated_at, deleted_at`,
		name, billingEmail, orgID,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy, &org.BillingEmail,
		&org.CreatedAt, &org.UpdatedAt, &org.DeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update organization: %w", err)
	}
	return org, nil
}

func (s *Store) SoftDeleteOrganization(ctx context.Context, orgID string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE organizations SET deleted_at = now(), updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL`,
		orgID,
	)
	if err != nil {
		return fmt.Errorf("delete organization: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("organization not found")
	}
	return nil
}

// GetOrganizationProject returns the project linked to an org (nil if not yet created).
func (s *Store) GetOrganizationProject(ctx context.Context, orgID string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, api_key, api_secret_hash, webhook_url, webhook_secret,
		        rate_limit_rps, max_retries, created_at
		 FROM projects WHERE org_id = $1 AND deleted_at IS NULL`,
		orgID,
	).Scan(&p.ID, &p.Name, &p.APIKey, &p.APISecretHash, &p.WebhookURL, &p.WebhookSecret,
		&p.RateLimitRPS, &p.MaxRetries, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get org project: %w", err)
	}
	return p, nil
}

// ── Org Members ───────────────────────────────────────────────────────────────

type OrgMember struct {
	ID           string
	OrgID        string
	ClerkUserID  string
	Role         string
	Email        string // joined from clerk_users
	FirstName    string
	LastName     string
	JoinedAt     time.Time
	DeletedAt    *time.Time
}

func (s *Store) AddOrgMember(ctx context.Context, orgID, clerkUserID, role string) (*OrgMember, error) {
	m := &OrgMember{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO org_members (org_id, clerk_user_id, role)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (org_id, clerk_user_id) DO UPDATE
		   SET deleted_at = NULL, role = EXCLUDED.role, joined_at = now()
		 RETURNING id, org_id, clerk_user_id, role, joined_at, deleted_at`,
		orgID, clerkUserID, role,
	).Scan(&m.ID, &m.OrgID, &m.ClerkUserID, &m.Role, &m.JoinedAt, &m.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("add org member: %w", err)
	}
	return m, nil
}

func (s *Store) GetOrgMember(ctx context.Context, orgID, clerkUserID string) (*OrgMember, error) {
	m := &OrgMember{}
	err := s.db.QueryRowContext(ctx,
		`SELECT m.id, m.org_id, m.clerk_user_id, m.role,
		        COALESCE(u.email,''), COALESCE(u.first_name,''), COALESCE(u.last_name,''),
		        m.joined_at, m.deleted_at
		 FROM org_members m
		 LEFT JOIN clerk_users u ON u.clerk_user_id = m.clerk_user_id
		 WHERE m.org_id = $1 AND m.clerk_user_id = $2 AND m.deleted_at IS NULL`,
		orgID, clerkUserID,
	).Scan(&m.ID, &m.OrgID, &m.ClerkUserID, &m.Role,
		&m.Email, &m.FirstName, &m.LastName, &m.JoinedAt, &m.DeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get org member: %w", err)
	}
	return m, nil
}

func (s *Store) ListOrgMembers(ctx context.Context, orgID string) ([]*OrgMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, m.org_id, m.clerk_user_id, m.role,
		        COALESCE(u.email,''), COALESCE(u.first_name,''), COALESCE(u.last_name,''),
		        m.joined_at, m.deleted_at
		 FROM org_members m
		 LEFT JOIN clerk_users u ON u.clerk_user_id = m.clerk_user_id
		 WHERE m.org_id = $1 AND m.deleted_at IS NULL
		 ORDER BY m.joined_at ASC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list org members: %w", err)
	}
	defer rows.Close()
	var members []*OrgMember
	for rows.Next() {
		m := &OrgMember{}
		if err := rows.Scan(&m.ID, &m.OrgID, &m.ClerkUserID, &m.Role,
			&m.Email, &m.FirstName, &m.LastName, &m.JoinedAt, &m.DeletedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// CountActiveOwners returns the number of active owners in an org.
// Used to prevent removing the last owner.
func (s *Store) CountActiveOwners(ctx context.Context, orgID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_members
		 WHERE org_id = $1 AND role = 'owner' AND deleted_at IS NULL`,
		orgID,
	).Scan(&count)
	return count, err
}

func (s *Store) UpdateOrgMemberRole(ctx context.Context, orgID, clerkUserID, role string) (*OrgMember, error) {
	m := &OrgMember{}
	err := s.db.QueryRowContext(ctx,
		`UPDATE org_members SET role = $1
		 WHERE org_id = $2 AND clerk_user_id = $3 AND deleted_at IS NULL
		 RETURNING id, org_id, clerk_user_id, role, joined_at, deleted_at`,
		role, orgID, clerkUserID,
	).Scan(&m.ID, &m.OrgID, &m.ClerkUserID, &m.Role, &m.JoinedAt, &m.DeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update member role: %w", err)
	}
	return m, nil
}

func (s *Store) RemoveOrgMember(ctx context.Context, orgID, clerkUserID string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE org_members SET deleted_at = now()
		 WHERE org_id = $1 AND clerk_user_id = $2 AND deleted_at IS NULL`,
		orgID, clerkUserID,
	)
	if err != nil {
		return fmt.Errorf("remove org member: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("member not found")
	}
	return nil
}

// ── Invites ───────────────────────────────────────────────────────────────────

type Invite struct {
	ID         string
	OrgID      string
	Email      string
	Token      string
	Role       string
	CreatedBy  string
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	AcceptedBy *string
	CreatedAt  time.Time
	DeletedAt  *time.Time
}

func (s *Store) CreateInvite(ctx context.Context, orgID, email, token, role, createdBy string) (*Invite, error) {
	inv := &Invite{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO invites (org_id, email, token, role, created_by)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, org_id, email, token, role, created_by, expires_at, accepted_at, accepted_by, created_at, deleted_at`,
		orgID, email, token, role, createdBy,
	).Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Token, &inv.Role, &inv.CreatedBy,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.AcceptedBy, &inv.CreatedAt, &inv.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

// GetInviteByToken fetches a pending, non-expired, non-deleted invite.
func (s *Store) GetInviteByToken(ctx context.Context, token string) (*Invite, error) {
	inv := &Invite{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org_id, email, token, role, created_by, expires_at, accepted_at, accepted_by, created_at, deleted_at
		 FROM invites
		 WHERE token = $1 AND deleted_at IS NULL`,
		token,
	).Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Token, &inv.Role, &inv.CreatedBy,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.AcceptedBy, &inv.CreatedAt, &inv.DeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get invite by token: %w", err)
	}
	return inv, nil
}

func (s *Store) ListOrgInvites(ctx context.Context, orgID string) ([]*Invite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, email, token, role, created_by, expires_at, accepted_at, accepted_by, created_at, deleted_at
		 FROM invites
		 WHERE org_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()
	var invites []*Invite
	for rows.Next() {
		inv := &Invite{}
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Token, &inv.Role, &inv.CreatedBy,
			&inv.ExpiresAt, &inv.AcceptedAt, &inv.AcceptedBy, &inv.CreatedAt, &inv.DeletedAt); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// AcceptInvite marks the invite accepted and returns the updated invite.
func (s *Store) AcceptInvite(ctx context.Context, token, acceptedBy string) (*Invite, error) {
	inv := &Invite{}
	err := s.db.QueryRowContext(ctx,
		`UPDATE invites
		 SET accepted_at = now(), accepted_by = $1
		 WHERE token = $2 AND deleted_at IS NULL AND accepted_at IS NULL
		 RETURNING id, org_id, email, token, role, created_by, expires_at, accepted_at, accepted_by, created_at, deleted_at`,
		acceptedBy, token,
	).Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Token, &inv.Role, &inv.CreatedBy,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.AcceptedBy, &inv.CreatedAt, &inv.DeletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("accept invite: %w", err)
	}
	return inv, nil
}

func (s *Store) RevokeInvite(ctx context.Context, orgID, token string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE invites SET deleted_at = now()
		 WHERE org_id = $1 AND token = $2 AND deleted_at IS NULL`,
		orgID, token,
	)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("invite not found")
	}
	return nil
}

// ── API Keys ──────────────────────────────────────────────────────────────────

type APIKey struct {
	ID          string
	ProjectID   string
	Name        string
	Key         string
	SecretHash  string
	CreatedBy   *string
	Active      bool
	LastUsedAt  *time.Time
	CreatedAt   time.Time
	DeletedAt   *time.Time
}

func (s *Store) CreateAPIKey(ctx context.Context, projectID, name, key, secretHash string, createdBy *string) (*APIKey, error) {
	ak := &APIKey{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO api_keys (project_id, name, key, secret_hash, created_by)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, project_id, name, key, secret_hash, created_by, active, last_used_at, created_at, deleted_at`,
		projectID, name, key, secretHash, createdBy,
	).Scan(&ak.ID, &ak.ProjectID, &ak.Name, &ak.Key, &ak.SecretHash, &ak.CreatedBy,
		&ak.Active, &ak.LastUsedAt, &ak.CreatedAt, &ak.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	return ak, nil
}

// GetAPIKeyByKey looks up an active API key and updates last_used_at.
// Returns the matching APIKey (without secret_hash) and the associated project.
func (s *Store) GetAPIKeyByKey(ctx context.Context, key string) (*APIKey, *Project, error) {
	ak := &APIKey{}
	p := &Project{}
	err := s.db.QueryRowContext(ctx,
		`SELECT ak.id, ak.project_id, ak.name, ak.key, ak.secret_hash, ak.created_by,
		        ak.active, ak.last_used_at, ak.created_at, ak.deleted_at,
		        p.id, p.name, p.api_key, p.api_secret_hash,
		        p.webhook_url, p.webhook_secret, p.rate_limit_rps, p.max_retries, p.created_at
		 FROM api_keys ak
		 JOIN projects p ON p.id = ak.project_id
		 WHERE ak.key = $1
		   AND ak.active = true
		   AND ak.deleted_at IS NULL
		   AND p.deleted_at IS NULL`,
		key,
	).Scan(
		&ak.ID, &ak.ProjectID, &ak.Name, &ak.Key, &ak.SecretHash, &ak.CreatedBy,
		&ak.Active, &ak.LastUsedAt, &ak.CreatedAt, &ak.DeletedAt,
		&p.ID, &p.Name, &p.APIKey, &p.APISecretHash,
		&p.WebhookURL, &p.WebhookSecret, &p.RateLimitRPS, &p.MaxRetries, &p.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get api key: %w", err)
	}
	// Touch last_used_at asynchronously — don't block the request.
	go func() {
		s.db.ExecContext(context.Background(), //nolint:errcheck
			`UPDATE api_keys SET last_used_at = now() WHERE id = $1`, ak.ID)
	}()
	return ak, p, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, projectID string) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, name, key, created_by, active, last_used_at, created_at, deleted_at
		 FROM api_keys
		 WHERE project_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	var keys []*APIKey
	for rows.Next() {
		ak := &APIKey{}
		if err := rows.Scan(&ak.ID, &ak.ProjectID, &ak.Name, &ak.Key, &ak.CreatedBy,
			&ak.Active, &ak.LastUsedAt, &ak.CreatedAt, &ak.DeletedAt); err != nil {
			return nil, err
		}
		keys = append(keys, ak)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, projectID, keyID string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET active = false, deleted_at = now()
		 WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`,
		keyID, projectID,
	)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}

// EnsureProjectForOrg creates a project for an org if none exists.
// Idempotent: returns existing project if already created.
func (s *Store) EnsureProjectForOrg(ctx context.Context, orgID, orgName string) (*Project, error) {
	existing, err := s.GetOrganizationProject(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	p := &Project{}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO projects (name, api_key, api_secret_hash, org_id, rate_limit_rps, max_retries)
		 VALUES ($1, '', '', $2, 100, 3)
		 ON CONFLICT (org_id) WHERE deleted_at IS NULL AND org_id IS NOT NULL DO NOTHING
		 RETURNING id, name, api_key, api_secret_hash, webhook_url, webhook_secret, rate_limit_rps, max_retries, created_at`,
		orgName+" Project", orgID,
	).Scan(&p.ID, &p.Name, &p.APIKey, &p.APISecretHash, &p.WebhookURL, &p.WebhookSecret,
		&p.RateLimitRPS, &p.MaxRetries, &p.CreatedAt)
	if err != nil {
		// Retry fetch — race with another request.
		return s.GetOrganizationProject(ctx, orgID)
	}
	return p, nil
}

// MarkWalletSwept updates swept_at for a wallet (used by sweep worker).
// func (s *Store) MarkWalletSwept(ctx context.Context, walletID string) error {
// 	_, err := s.db.ExecContext(ctx,
// 		`UPDATE wallets SET swept_at = now() WHERE id = $1`, walletID)
// 	return err
// }