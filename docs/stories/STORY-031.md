# STORY-031: RBAC Permission System + Product Line Isolation

**Epic:** EPIC-006 (System Management & Permissions)
**Priority:** Must Have
**Story Points:** 8
**Status:** Not Started
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 5

---

## User Story

As a system admin,
I want role-based access control with product-line data isolation,
So that each team only accesses their own data.

---

## Description

### Background
UNICA manages 7-8 product lines, each with its own channels, knowledge bases, and agent teams. Without RBAC, any user can access any product line's data, creating security and compliance risks. This story implements the permission backbone that all admin features (STORY-032, STORY-033) depend on.

### Scope
**In scope:**
- Role definitions: SuperAdmin, ProductAdmin, Supervisor, Agent, KnowledgeAdmin
- Product line scoping for all roles (except SuperAdmin)
- JWT-based authentication with role + product_line_ids claims
- PostgreSQL schema for users, roles, product_lines, user_roles
- PostgreSQL Row-Level Security (RLS) policies for defense-in-depth
- API middleware for role/permission checking
- Chatwoot Account-per-product-line mapping for RBAC mirroring
- Admin API endpoints for user and role management

**Out of scope:**
- SSO/LDAP integration (future enhancement)
- Fine-grained field-level permissions
- Audit logging of permission changes (STORY-034)

### User Flow

**Admin creating a user:**
1. SuperAdmin opens admin panel
2. Creates new user with name, email, password
3. Assigns role(s) scoped to product line(s)
4. User receives credentials
5. User logs in, JWT issued with role + product_line_ids
6. All API calls filtered by product line scope

**Data isolation in action:**
1. ProductAdmin for "Brand A" calls GET /api/conversations
2. Middleware extracts JWT claims: role=ProductAdmin, product_lines=[brand_a]
3. Query filtered to only Brand A conversations (via RLS or WHERE clause)
4. Brand B data never returned

---

## Acceptance Criteria

- [ ] Roles defined: SuperAdmin, ProductAdmin, Supervisor, Agent, KnowledgeAdmin
- [ ] Each role has explicit permission set (see Permission Matrix below)
- [ ] Roles scoped to one or more product lines (except SuperAdmin which is global)
- [ ] Cross-product-line data access blocked at API level
- [ ] JWT token includes: user_id, role, product_line_ids, exp
- [ ] JWT signing with RS256 or HS256 (configurable secret)
- [ ] Login endpoint: POST /api/v1/auth/login returns JWT
- [ ] Token refresh endpoint: POST /api/v1/auth/refresh
- [ ] API middleware validates JWT and enforces role permissions
- [ ] PostgreSQL RLS policies enforce product_line isolation as defense-in-depth
- [ ] Chatwoot Account mapping: one Account per product line, agents assigned to matching Account
- [ ] Admin CRUD APIs: users, roles, product_lines, user_role assignments
- [ ] SuperAdmin can manage all product lines
- [ ] Invalid/expired JWT returns 401 Unauthorized
- [ ] Insufficient permissions returns 403 Forbidden
- [ ] Unit tests for permission checking logic
- [ ] Integration tests for API authorization

---

## Technical Notes

### Database Schema

```sql
-- Product lines
CREATE TABLE product_lines (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL UNIQUE,
    display_name VARCHAR(200) NOT NULL,
    chatwoot_account_id INT,
    dify_workspace_id VARCHAR(100),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Users
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    display_name VARCHAR(200) NOT NULL,
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Roles (predefined, seeded)
CREATE TABLE roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(50) NOT NULL UNIQUE,  -- super_admin, product_admin, supervisor, agent, knowledge_admin
    description TEXT
);

-- User-Role-ProductLine mapping (many-to-many-to-many)
CREATE TABLE user_roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    role_id UUID NOT NULL REFERENCES roles(id),
    product_line_id UUID REFERENCES product_lines(id),  -- NULL for SuperAdmin (global)
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, role_id, product_line_id)
);

-- Enable RLS on key tables
ALTER TABLE conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE customers ENABLE ROW LEVEL SECURITY;

-- RLS policy example
CREATE POLICY product_line_isolation ON conversations
    USING (product_line_id IN (
        SELECT product_line_id FROM user_roles
        WHERE user_id = current_setting('app.current_user_id')::UUID
    ));
```

### Permission Matrix

| Permission | SuperAdmin | ProductAdmin | Supervisor | Agent | KnowledgeAdmin |
|-----------|:---:|:---:|:---:|:---:|:---:|
| Manage users & roles | Yes | Own PL | No | No | No |
| Manage channels | Yes | Own PL | No | No | No |
| Manage AI config | Yes | Own PL | No | No | Own PL |
| Manage knowledge base | Yes | Own PL | No | No | Own PL |
| View all conversations | Yes | Own PL | Own PL | No | No |
| Handle conversations | Yes | Own PL | Own PL | Own PL | No |
| View reports | Yes | Own PL | Own PL | No | No |
| Manage product lines | Yes | No | No | No | No |

### Components
- **New files:**
  - `unica/admin/internal/auth/jwt.go` - JWT generation, validation, claims
  - `unica/admin/internal/auth/middleware.go` - HTTP middleware for auth + RBAC
  - `unica/admin/internal/auth/password.go` - bcrypt password hashing
  - `unica/admin/internal/rbac/roles.go` - Role definitions and permission checks
  - `unica/admin/internal/rbac/policy.go` - Permission matrix enforcement
  - `unica/admin/internal/handler/auth.go` - Login/refresh endpoints
  - `unica/admin/internal/handler/users.go` - User CRUD endpoints
  - `unica/admin/internal/handler/roles.go` - Role assignment endpoints
  - `unica/admin/internal/handler/product_lines.go` - Product line CRUD
  - `unica/admin/internal/repository/user.go` - User DB operations
  - `unica/admin/internal/repository/role.go` - Role DB operations
  - `unica/admin/internal/repository/product_line.go` - Product line DB operations
  - `unica/admin/cmd/admin/main.go` - Full admin service startup (replace stub)
  - `migrations/007_rbac.sql` - Schema migration
  - Test files for all packages
- **Modified files:**
  - `unica/router/` - Add product_line_id context to message processing
  - `unica/gateway/` - Pass product_line context from channel config

### API Endpoints

```
POST   /api/v1/auth/login              - Login, returns JWT
POST   /api/v1/auth/refresh            - Refresh JWT

GET    /api/v1/users                   - List users (filtered by PL scope)
POST   /api/v1/users                   - Create user
GET    /api/v1/users/:id               - Get user
PUT    /api/v1/users/:id               - Update user
DELETE /api/v1/users/:id               - Deactivate user

POST   /api/v1/users/:id/roles         - Assign role to user
DELETE /api/v1/users/:id/roles/:roleId  - Remove role from user

GET    /api/v1/product-lines           - List product lines
POST   /api/v1/product-lines           - Create product line (SuperAdmin)
PUT    /api/v1/product-lines/:id       - Update product line
```

### JWT Claims Structure
```go
type Claims struct {
    UserID        string   `json:"user_id"`
    Email         string   `json:"email"`
    Role          string   `json:"role"`            // highest role
    Roles         []string `json:"roles"`           // all roles
    ProductLineIDs []string `json:"product_line_ids"` // scoped PLs (empty = global)
    jwt.RegisteredClaims
}
```

### Security Considerations
- Passwords hashed with bcrypt (cost=12)
- JWT expiry: access token 2h, refresh token 7d
- Refresh tokens stored in Redis with revocation support
- RLS policies as defense-in-depth (not sole enforcement)
- SQL injection prevention via parameterized queries
- Chatwoot agent assignments synced when roles change

---

## Dependencies

**Prerequisite Stories:**
- STORY-001: PostgreSQL (Done)
- STORY-003: Chatwoot (Done) - for Account mapping

**Blocked Stories:**
- STORY-032: Channel Configuration CRUD (depends on RBAC middleware)
- STORY-033: AI Agent Configuration UI (depends on RBAC middleware)
- STORY-034: Audit Logging (depends on RBAC user context)

**External Dependencies:**
- None

---

## Definition of Done

- [ ] Code implemented and committed to feature branch
- [ ] Database migration applied successfully
- [ ] Unit tests written and passing
  - [ ] JWT generation and validation tests
  - [ ] Password hashing tests
  - [ ] Permission matrix tests (all role combinations)
  - [ ] Middleware tests (valid/invalid/expired JWT, insufficient perms)
  - [ ] Repository CRUD tests
- [ ] Integration tests passing
  - [ ] Login flow end-to-end
  - [ ] RBAC enforcement on API calls
  - [ ] Cross-product-line access blocked
  - [ ] RLS policies verified
- [ ] Admin service deploys successfully to K3s dev environment
- [ ] Chatwoot Account mapping verified
- [ ] Acceptance criteria validated
- [ ] No critical or high severity bugs

---

## Story Points Breakdown

- **DB schema + migration**: 1 point
- **JWT auth (login/refresh/middleware)**: 2 points
- **RBAC logic + permission matrix**: 2 points
- **Admin CRUD APIs**: 1.5 points
- **Testing**: 1.5 points
- **Total:** 8 points

**Rationale:** Most complex story in Sprint 5. Involves new service (admin), database schema, auth system, and permission enforcement across multiple layers. Foundation for all future admin features.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created

**Actual Effort:** TBD

---

**This story was created using BMAD Method v6 - Phase 4 (Implementation Planning)**
