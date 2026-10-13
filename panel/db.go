package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Names the panel creates: lowercase, so they never need quoting by clients.
var validName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

var reservedNames = map[string]bool{"postgres": true, "template0": true, "template1": true, "public": true}

type DB struct {
	pool *pgxpool.Pool
	// conn is the pool's connection config, to open one-off connections to
	// other databases (privileges on tables are granted from inside each one).
	conn *pgx.ConnConfig
}

func NewDB(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 2
	cfg.MaxConnIdleTime = time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "just-a-db-panel"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool, conn: cfg.ConnConfig}, nil
}

func (d *DB) Close() { d.pool.Close() }

type ServerInfo struct {
	Version        string
	MaxConnections int
	Connections    int
	StartedAt      time.Time
}

type Database struct {
	Name        string
	Owner       string
	Size        int64
	Connections int
	// PUBLIC can connect: any role (e.g. another database's user) can open it.
	PublicConnect bool
}

type Role struct {
	Name      string
	Superuser bool
	CanLogin  bool
	Owns      []string
	// Databases it was granted access to without owning them.
	Access []string
}

func (d *DB) ServerInfo(ctx context.Context) (ServerInfo, error) {
	var s ServerInfo
	err := d.pool.QueryRow(ctx, `
		SELECT current_setting('server_version'), current_setting('max_connections')::int,
		       (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend')::int,
		       pg_postmaster_start_time()`).Scan(&s.Version, &s.MaxConnections, &s.Connections, &s.StartedAt)
	return s, err
}

func (d *DB) Databases(ctx context.Context) ([]Database, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT d.datname, pg_get_userbyid(d.datdba), pg_database_size(d.datname),
		       (SELECT count(*) FROM pg_stat_activity a WHERE a.datname = d.datname)::int,
		       has_database_privilege('public', d.datname, 'CONNECT')
		FROM pg_database d
		WHERE NOT d.datistemplate AND d.datname <> 'postgres'
		ORDER BY d.datname`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Database, error) {
		var db Database
		err := r.Scan(&db.Name, &db.Owner, &db.Size, &db.Connections, &db.PublicConnect)
		return db, err
	})
}

func (d *DB) Roles(ctx context.Context) ([]Role, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT r.rolname, r.rolsuper, r.rolcanlogin,
		       coalesce((SELECT array_agg(d.datname ORDER BY d.datname) FROM pg_database d
		                 WHERE d.datdba = r.oid AND NOT d.datistemplate AND d.datname <> 'postgres'), '{}'),
		       coalesce((SELECT array_agg(DISTINCT d.datname) FROM pg_database d, aclexplode(d.datacl) a
		                 WHERE a.grantee = r.oid AND a.privilege_type = 'CONNECT' AND d.datdba <> r.oid
		                   AND NOT d.datistemplate), '{}')
		FROM pg_roles r
		WHERE r.rolname !~ '^pg_'
		ORDER BY r.rolsuper DESC, r.rolname`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Role, error) {
		var role Role
		err := r.Scan(&role.Name, &role.Superuser, &role.CanLogin, &role.Owns, &role.Access)
		return role, err
	})
}

func (d *DB) databaseExists(ctx context.Context, name string) (bool, error) {
	var ok bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	return ok, err
}

func (d *DB) role(ctx context.Context, name string) (exists, superuser bool, err error) {
	err = d.pool.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = $1`, name).Scan(&superuser)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, superuser, err
}

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

// newPassword is hex, so it's safe inside a SQL literal and in connection URLs.
func newPassword() string { return randomHex(24) }

// CreateDatabase creates a database owned by a new login role of the same
// name, closed to every other role. Returns the role's password.
func (d *DB) CreateDatabase(ctx context.Context, name string) (string, error) {
	if !validName.MatchString(name) || reservedNames[name] {
		return "", fmt.Errorf("nombre inválido: usa minúsculas, números y _, empezando por letra (2 a 41 caracteres)")
	}
	if exists, err := d.databaseExists(ctx, name); err != nil {
		return "", err
	} else if exists {
		return "", fmt.Errorf("ya existe una base de datos llamada %q", name)
	}
	if exists, _, err := d.role(ctx, name); err != nil {
		return "", err
	} else if exists {
		return "", fmt.Errorf("ya existe un usuario llamado %q", name)
	}

	password := newPassword()
	// DDL can't take bind parameters; the name is validated and quoted, and
	// the password is hex.
	if _, err := d.pool.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, ident(name), password)); err != nil {
		return "", err
	}
	// CREATE DATABASE can't run inside a transaction, so undo by hand.
	steps := []string{
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, ident(name), ident(name)),
		fmt.Sprintf(`REVOKE ALL ON DATABASE %s FROM PUBLIC`, ident(name)),
	}
	for _, sql := range steps {
		if _, err := d.pool.Exec(ctx, sql); err != nil {
			_, _ = d.pool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, ident(name)))
			_, _ = d.pool.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, ident(name)))
			return "", err
		}
	}
	return password, nil
}

// CreateUser creates a login role with access to existing databases it
// doesn't own: read-write (SELECT/INSERT/UPDATE/DELETE, sequences) or
// read-only, on today's tables and on the ones each database's owner creates
// later. It can't create tables or change the schema. Returns the password.
func (d *DB) CreateUser(ctx context.Context, name string, databases []string, readOnly bool) (string, error) {
	if !validName.MatchString(name) || reservedNames[name] {
		return "", fmt.Errorf("nombre inválido: usa minúsculas, números y _, empezando por letra (2 a 41 caracteres)")
	}
	if len(databases) == 0 {
		return "", fmt.Errorf("elige al menos una base de datos")
	}
	if exists, _, err := d.role(ctx, name); err != nil {
		return "", err
	} else if exists {
		return "", fmt.Errorf("ya existe un usuario llamado %q", name)
	}
	owners := map[string]string{}
	for _, db := range databases {
		var owner string
		err := d.pool.QueryRow(ctx, `SELECT pg_get_userbyid(datdba) FROM pg_database
			WHERE datname = $1 AND NOT datistemplate AND datname <> 'postgres'`, db).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("la base de datos %q no existe", db)
		} else if err != nil {
			return "", err
		}
		owners[db] = owner
	}

	password := newPassword()
	if _, err := d.pool.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, ident(name), password)); err != nil {
		return "", err
	}
	for i, db := range databases {
		if err := d.grantAccess(ctx, db, owners[db], name, readOnly); err != nil {
			// Each database's grants are one transaction: undo the ones that
			// went through, then the role.
			for _, done := range databases[:i] {
				if err := d.inDatabase(ctx, done, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %s`, ident(name)))
					return err
				}); err != nil {
					log.Printf("undo grants of %q on %q: %v", name, done, err)
				}
			}
			if _, err := d.pool.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, ident(name))); err != nil {
				log.Printf("drop role %q after failed grants: %v", name, err)
			}
			return "", fmt.Errorf("no se pudo dar acceso a %s: %w", db, err)
		}
	}
	return password, nil
}

// grantAccess runs inside the database: table privileges live there.
func (d *DB) grantAccess(ctx context.Context, db, owner, user string, readOnly bool) error {
	dbPrivs, tablePrivs, seqPrivs := "CONNECT, TEMPORARY", "SELECT, INSERT, UPDATE, DELETE", "USAGE, SELECT, UPDATE"
	if readOnly {
		dbPrivs, tablePrivs, seqPrivs = "CONNECT", "SELECT", "SELECT"
	}
	return d.inDatabase(ctx, db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT nspname FROM pg_namespace
			WHERE nspname !~ '^pg_' AND nspname <> 'information_schema' ORDER BY nspname`)
		if err != nil {
			return err
		}
		schemas, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		u, o := ident(user), ident(owner)
		sql := []string{
			fmt.Sprintf(`GRANT %s ON DATABASE %s TO %s`, dbPrivs, ident(db), u),
			// Whatever the owner creates later, in any schema.
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s GRANT USAGE ON SCHEMAS TO %s`, o, u),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s GRANT %s ON TABLES TO %s`, o, tablePrivs, u),
			fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s GRANT %s ON SEQUENCES TO %s`, o, seqPrivs, u),
		}
		for _, s := range schemas {
			sql = append(sql,
				fmt.Sprintf(`GRANT USAGE ON SCHEMA %s TO %s`, ident(s), u),
				fmt.Sprintf(`GRANT %s ON ALL TABLES IN SCHEMA %s TO %s`, tablePrivs, ident(s), u),
				fmt.Sprintf(`GRANT %s ON ALL SEQUENCES IN SCHEMA %s TO %s`, seqPrivs, ident(s), u),
			)
		}
		for _, q := range sql {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

// inDatabase runs fn in a transaction on a one-off connection to db.
func (d *DB) inDatabase(ctx context.Context, db string, fn func(pgx.Tx) error) error {
	cfg := d.conn.Copy()
	cfg.Database = db
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	return pgx.BeginFunc(ctx, conn, fn)
}

// ResetPassword sets a new password for a non-superuser login role. The
// superuser's password is also in .db.env and pgbouncer's userlist, so it's
// changed by hand.
func (d *DB) ResetPassword(ctx context.Context, name string) (string, error) {
	exists, super, err := d.role(ctx, name)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("el usuario %q no existe", name)
	}
	if super {
		return "", fmt.Errorf("la contraseña del superusuario se cambia a mano (también está en .db.env)")
	}
	password := newPassword()
	if _, err := d.pool.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s WITH LOGIN PASSWORD '%s'`, ident(name), password)); err != nil {
		return "", err
	}
	return password, nil
}

// RestrictDatabase closes a database to every role but its owner (and
// superusers).
func (d *DB) RestrictDatabase(ctx context.Context, name string) error {
	if exists, err := d.databaseExists(ctx, name); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("la base de datos %q no existe", name)
	}
	if _, err := d.pool.Exec(ctx, fmt.Sprintf(`REVOKE CONNECT, TEMPORARY ON DATABASE %s FROM PUBLIC`, ident(name))); err != nil {
		return err
	}
	// CONNECT is only checked when a session starts, and pgbouncer keeps
	// sessions open: close the ones other roles already have.
	_, err := d.pool.Exec(ctx, `
		SELECT pg_terminate_backend(a.pid)
		FROM pg_stat_activity a
		JOIN pg_database d ON d.datname = a.datname
		JOIN pg_roles r ON r.rolname = a.usename
		WHERE a.datname = $1 AND a.usename <> pg_get_userbyid(d.datdba) AND NOT r.rolsuper`, name)
	return err
}

// DropDatabase drops a database, closing its open connections, and optionally
// its owner role when it isn't a superuser.
func (d *DB) DropDatabase(ctx context.Context, name string, dropOwner bool) (string, error) {
	if reservedNames[name] {
		return "", fmt.Errorf("%q no se puede borrar", name)
	}
	var owner string
	err := d.pool.QueryRow(ctx, `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = $1`, name).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("la base de datos %q no existe", name)
	} else if err != nil {
		return "", err
	}
	if _, err := d.pool.Exec(ctx, fmt.Sprintf(`DROP DATABASE %s WITH (FORCE)`, ident(name))); err != nil {
		return "", err
	}
	if !dropOwner {
		return "", nil
	}
	_, super, err := d.role(ctx, owner)
	if err != nil {
		return "", err
	}
	if super {
		return fmt.Sprintf("El dueño %q es superusuario, no se borró.", owner), nil
	}
	if _, err := d.pool.Exec(ctx, fmt.Sprintf(`DROP ROLE %s`, ident(owner))); err != nil {
		return fmt.Sprintf("La base se borró, pero el usuario %q no: %v", owner, err), nil
	}
	return fmt.Sprintf("También se borró el usuario %q.", owner), nil
}
