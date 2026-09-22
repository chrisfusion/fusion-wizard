// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

import (
	"context"
	"net/http"
	"strings"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	headerUserID    = "X-User-ID"
	headerUserEmail = "X-User-Email"
	maxHeaderLen    = 256
)

// Principal is an authenticated Kubernetes service account.
type Principal struct{ Namespace, Name string }

func (p Principal) String() string { return p.Namespace + "/" + p.Name }

// Authenticator validates a bearer token. It returns (nil, nil) for a token that is not valid, and
// an error only when the check itself could not be performed.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*Principal, error)
}

// TokenReviewAuthenticator validates service-account tokens with the Kubernetes TokenReview API.
type TokenReviewAuthenticator struct {
	KubeClient kubernetes.Interface
	Audiences  []string
}

func (a *TokenReviewAuthenticator) Authenticate(ctx context.Context, token string) (*Principal, error) {
	tr, err := a.KubeClient.AuthenticationV1().TokenReviews().Create(ctx, &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{Token: token, Audiences: a.Audiences},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if !tr.Status.Authenticated {
		LoggerFromCtx(ctx).Warn("token review: not authenticated", "error", tr.Status.Error, "audiences_requested", a.Audiences)
		return nil, nil
	}
	// Only service accounts are accepted: "system:serviceaccount:<namespace>:<name>".
	parts := strings.Split(tr.Status.User.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || parts[2] == "" || parts[3] == "" {
		LoggerFromCtx(ctx).Warn("token review: unexpected username format", "username", tr.Status.User.Username)
		return nil, nil
	}
	return &Principal{Namespace: parts[2], Name: parts[3]}, nil
}

// Caller is who is making the request: the authenticated service account and, when a trusted BFF
// vouches for one, the human user behind it.
type Caller struct {
	Principal string // "<namespace>/<name>", or "anonymous" in unauthenticated dev mode
	UserID    string
	UserEmail string
}

type callerKey struct{}

// CallerFromContext returns the caller stored by the auth middleware.
func CallerFromContext(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// authMiddleware authenticates the request and records the caller. Only an authenticated,
// allowlisted service account (a BFF) gets to state a user identity through X-User-ID and
// X-User-Email; nobody else's headers are ever read, so a user cannot spoof another user.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := LoggerFromCtx(r.Context())
		caller := Caller{Principal: "anonymous"}

		if !s.cfg.AllowUnauthenticated {
			token, ok := bearerToken(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			p, err := s.authn.Authenticate(r.Context(), token)
			if err != nil {
				log.Error("token review failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "authentication is temporarily unavailable")
				return
			}
			if p == nil {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			if _, ok := s.allowed[p.String()]; !ok {
				log.Warn("service account is not allowed", "service_account", p.String())
				writeError(w, http.StatusForbidden, "service account is not allowed to use this API")
				return
			}
			caller.Principal = p.String()
		}

		var ok bool
		if caller.UserID, ok = headerValue(r, headerUserID); !ok {
			writeError(w, http.StatusBadRequest, "invalid "+headerUserID+" header")
			return
		}
		if caller.UserEmail, ok = headerValue(r, headerUserEmail); !ok {
			writeError(w, http.StatusBadRequest, "invalid "+headerUserEmail+" header")
			return
		}

		log = log.With("principal", caller.Principal)
		if caller.UserID != "" {
			log = log.With("user_id", caller.UserID)
		}
		ctx := context.WithValue(WithLogger(r.Context(), log), callerKey{}, caller)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

// headerValue returns the trimmed header value. Empty is fine (no user stated); overlong values and
// control characters are rejected because the value is stored on the run and shown in logs.
func headerValue(r *http.Request, name string) (string, bool) {
	v := strings.TrimSpace(r.Header.Get(name))
	if len(v) > maxHeaderLen {
		return "", false
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	return v, true
}
