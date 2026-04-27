package chat

// Visitor chat-session JWT. Issued by /chat/start, presented by
// the visitor's WebSocket connect to /chat/ws. Lifetime is short
// (30 min default) and the token is bound to a single session_id;
// reissue via /chat/start when it expires.
//
// Token shape (HS256, signed with the existing config.jwt_key):
//
//	{
//	  "sub":  "chat:visitor",
//	  "sid":  "<session_id>",
//	  "vid":  "<visitor_id>",
//	  "srv":  "<server_id>",
//	  "email":"<visitor_email>",
//	  "iat":  <issued unix>,
//	  "exp":  <issued + ttl unix>
//	}
//
// `email` is denormalised onto the token so the WS handler doesn't
// need a DB roundtrip on every connect.

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// DefaultTokenTTL is the chat-session JWT's lifetime.
const DefaultTokenTTL = 30 * time.Minute

// TokenSubject is the constant `sub` claim — the WS handler refuses
// any token without exactly this subject so a stolen admin JWT
// can't be repurposed at /chat/ws.
const TokenSubject = "chat:visitor"

// ChatClaims is the parsed token shape. SessionID/VisitorID/ServerID
// come from `sid`/`vid`/`srv`.
type ChatClaims struct {
	jwt.RegisteredClaims
	SessionID    string `json:"sid"`
	VisitorID    string `json:"vid"`
	ServerID     string `json:"srv"`
	VisitorEmail string `json:"email"`
}

// IssueToken signs and returns a chat token for a session. ttl=0
// substitutes DefaultTokenTTL.
func IssueToken(jwtKey string, sessionID uuid.UUID, visitorID, serverID, email string, ttl time.Duration) (string, time.Time, error) {
	if jwtKey == "" {
		return "", time.Time{}, errors.New("chat: missing jwt key")
	}
	if sessionID == uuid.Nil || serverID == "" {
		return "", time.Time{}, errors.New("chat: session_id and server_id required")
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)
	claims := &ChatClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   TokenSubject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		SessionID:    sessionID.String(),
		VisitorID:    visitorID,
		ServerID:     serverID,
		VisitorEmail: email,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(jwtKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("chat: sign token: %w", err)
	}
	return signed, exp, nil
}

// ParseToken validates the signature, expiry, and subject. Returns
// the claims on success.
func ParseToken(jwtKey, token string) (*ChatClaims, error) {
	if jwtKey == "" {
		return nil, errors.New("chat: missing jwt key")
	}
	claims := &ChatClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("chat: unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(jwtKey), nil
	})
	if err != nil {
		return nil, fmt.Errorf("chat: parse token: %w", err)
	}
	if !parsed.Valid {
		return nil, errors.New("chat: invalid token")
	}
	if claims.Subject != TokenSubject {
		return nil, fmt.Errorf("chat: wrong subject %q", claims.Subject)
	}
	if claims.SessionID == "" || claims.ServerID == "" {
		return nil, errors.New("chat: token missing sid/srv")
	}
	if _, err := uuid.Parse(claims.SessionID); err != nil {
		return nil, fmt.Errorf("chat: token bad sid: %w", err)
	}
	return claims, nil
}
