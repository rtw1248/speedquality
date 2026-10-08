package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

type ActivationClaims struct {
	Issuer               string `json:"iss"`
	Audience             string `json:"aud"`
	Subject              string `json:"sub"`
	JWTID                string `json:"jti"`
	Scope                string `json:"scope"`
	ClientIP             string `json:"client_ip"`
	TargetMbps           int    `json:"target_mbps"`
	MaxBytesPerDirection int64  `json:"max_bytes"`
	DurationSeconds      int    `json:"duration_seconds"`
	IssuedAt             int64  `json:"iat"`
	NotBefore            int64  `json:"nbf"`
	ExpiresAt            int64  `json:"exp"`
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid,omitempty"`
}

func verifyActivationJWT(token string, config Config, now time.Time) (ActivationClaims, error) {
	var claims ActivationClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 4096 {
		return claims, errors.New("JWT 格式无效")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, errors.New("JWT 头无效")
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "ES256" || header.Type != "JWT" {
		return claims, errors.New("JWT 算法无效")
	}
	if config.JWTPublicKey.Kid != "" && header.KeyID != config.JWTPublicKey.Kid {
		return claims, errors.New("JWT 密钥编号无效")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 4096 {
		return claims, errors.New("JWT 载荷无效")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, errors.New("JWT 载荷无效")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return claims, errors.New("JWT 签名无效")
	}
	publicKey, err := publicKeyFromJWK(config.JWTPublicKey)
	if err != nil {
		return claims, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(publicKey, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return claims, errors.New("JWT 签名校验失败")
	}
	nowUnix := now.Unix()
	if claims.Issuer != config.JWTIssuer || claims.Audience != config.JWTAudience ||
		claims.Subject != config.NodeID || claims.ExpiresAt <= nowUnix ||
		claims.NotBefore > nowUnix+5 || claims.IssuedAt > nowUnix+30 ||
		claims.ExpiresAt-claims.IssuedAt > 15*60 {
		return claims, errors.New("JWT 约束不匹配")
	}
	if claims.Scope != "public" && claims.Scope != "owner" {
		return claims, errors.New("JWT scope 无效")
	}
	if len(claims.JWTID) < 12 || len(claims.JWTID) > 128 || strings.ContainsAny(claims.JWTID, "\r\n") {
		return claims, errors.New("JWT jti 无效")
	}
	validTarget := claims.TargetMbps == 100 || claims.TargetMbps == 200 || claims.TargetMbps == 400
	if net.ParseIP(claims.ClientIP) == nil || !validTarget || claims.TargetMbps > config.MaxMbps ||
		claims.DurationSeconds != 5 || claims.MaxBytesPerDirection < 1 ||
		claims.MaxBytesPerDirection > 2_000_000_000 {
		return claims, errors.New("JWT 测速参数无效")
	}
	return claims, nil
}

func publicKeyFromJWK(jwk PublicJWK) (*ecdsa.PublicKey, error) {
	if jwk.Kty != "EC" || jwk.Crv != "P-256" || (jwk.Alg != "" && jwk.Alg != "ES256") {
		return nil, errors.New("平台 JWT 公钥格式无效")
	}
	xBytes, errX := base64.RawURLEncoding.DecodeString(jwk.X)
	yBytes, errY := base64.RawURLEncoding.DecodeString(jwk.Y)
	if errX != nil || errY != nil || len(xBytes) != 32 || len(yBytes) != 32 {
		return nil, errors.New("平台 JWT 公钥坐标无效")
	}
	key := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}
	if !key.Curve.IsOnCurve(key.X, key.Y) {
		return nil, fmt.Errorf("平台 JWT 公钥不在 %s 曲线上", jwk.Crv)
	}
	return key, nil
}
