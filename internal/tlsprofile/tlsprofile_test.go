package tlsprofile

import (
	"reflect"
	"testing"
)

func TestParseProtocols(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []Version
		wantErr bool
	}{
		{
			name: "the old ssl.conf default",
			raw:  "all -SSLv2 -SSLv3 -TLSv1 -TLSv1.1",
			want: []Version{VersionTLS12, VersionTLS13},
		},
		{
			name: "TLS 1.3 only, as a post-quantum safe profile would ask for",
			raw:  "-all +TLSv1.3",
			want: []Version{VersionTLS13},
		},
		{
			name: "a bare version enables it",
			raw:  "-all TLSv1.2 TLSv1.3",
			want: []Version{VersionTLS12, VersionTLS13},
		},
		{
			name: "TLSv1 and TLSv1.0 name the same version",
			raw:  "-all +TLSv1 +TLSv1.0",
			want: []Version{VersionTLS10},
		},
		{
			name: "tokens apply left to right, so a later one wins",
			raw:  "-all +TLSv1.2 -TLSv1.2 +TLSv1.3",
			want: []Version{VersionTLS13},
		},
		{
			name: "case does not matter",
			raw:  "-ALL +tlsv1.3",
			want: []Version{VersionTLS13},
		},
		{
			name: "dead protocols are accepted but never enabled",
			raw:  "-all +SSLv3 +TLSv1.3",
			want: []Version{VersionTLS13},
		},
		{
			name:    "a profile enabling nothing we support is an error",
			raw:     "-all +SSLv3",
			wantErr: true,
		},
		{
			name:    "an unknown token is an error",
			raw:     "-all +TLSv1.4",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProtocols(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseProtocols(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseProtocols(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseCiphers(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantCiphers []string
		wantSuites  []string
		wantErr     bool
	}{
		{
			name:        "TLS 1.3 ciphersuites are split out by their TLS_ prefix",
			raw:         "TLS_AES_256_GCM_SHA384:ECDHE-RSA-AES128-GCM-SHA256",
			wantCiphers: []string{"ECDHE-RSA-AES128-GCM-SHA256"},
			wantSuites:  []string{"TLS_AES_256_GCM_SHA384"},
		},
		{
			name:        "the old ssl.conf default, operators and all",
			raw:         "ECDHE+AESGCM:DHE+AESGCM:!aNULL:!MD5:!RC4:!3DES",
			wantCiphers: []string{"ECDHE+AESGCM", "DHE+AESGCM", "!aNULL", "!MD5", "!RC4", "!3DES"},
		},
		{
			name:        "surrounding whitespace and empty entries are dropped",
			raw:         " ECDHE-RSA-AES128-GCM-SHA256 : : ",
			wantCiphers: []string{"ECDHE-RSA-AES128-GCM-SHA256"},
		},
		{
			name:    "a value that could break out of the rendered config is an error",
			raw:     `ECDHE-RSA-AES128-GCM-SHA256" $(id)`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ciphers, suites, err := parseCiphers(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCiphers(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(ciphers, tt.wantCiphers) {
				t.Errorf("parseCiphers(%q) ciphers = %v, want %v", tt.raw, ciphers, tt.wantCiphers)
			}
			if !reflect.DeepEqual(suites, tt.wantSuites) {
				t.Errorf("parseCiphers(%q) cipherSuites = %v, want %v", tt.raw, suites, tt.wantSuites)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Run("an absent key leaves that part of the profile unset", func(t *testing.T) {
		got, err := parse(map[string]string{"SSLProtocol": "-all +TLSv1.3"})
		if err != nil {
			t.Fatalf("parse() error = %v", err)
		}
		if got.Ciphers != nil || got.CipherSuites != nil {
			t.Errorf("parse() ciphers = %v/%v, want both nil", got.Ciphers, got.CipherSuites)
		}
	})

	t.Run("an empty ConfigMap is the zero profile", func(t *testing.T) {
		got, err := parse(nil)
		if err != nil {
			t.Fatalf("parse() error = %v", err)
		}
		if !reflect.DeepEqual(got, Profile{}) {
			t.Errorf("parse(nil) = %+v, want zero Profile", got)
		}
	})
}

func TestRenderers(t *testing.T) {
	// modern - a post-quantum safe profile: TLS 1.3 only
	modern := Profile{
		Versions:     []Version{VersionTLS13},
		CipherSuites: []string{"TLS_AES_256_GCM_SHA384", "TLS_AES_128_GCM_SHA256"},
	}
	// intermediate - TLS 1.2 and 1.3, the shape the default profile has
	intermediate := Profile{
		Versions:     []Version{VersionTLS12, VersionTLS13},
		Ciphers:      []string{"ECDHE-RSA-AES128-GCM-SHA256"},
		CipherSuites: []string{"TLS_AES_256_GCM_SHA384"},
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"zero profile renders nothing for Redis protocols", Profile{}.RedisProtocols(), ""},
		{"zero profile renders nothing for Redis ciphers", Profile{}.RedisCiphers(), ""},
		{"zero profile renders nothing for memcached", Profile{}.MemcachedMinVersion(), ""},
		{"Redis protocols are space separated", intermediate.RedisProtocols(), "TLSv1.2 TLSv1.3"},
		{"Redis ciphers are colon separated", intermediate.RedisCiphers(), "ECDHE-RSA-AES128-GCM-SHA256"},
		{"Redis ciphersuites are colon separated", modern.RedisCipherSuites(), "TLS_AES_256_GCM_SHA384:TLS_AES_128_GCM_SHA256"},
		{"memcached takes the lowest version as its floor", intermediate.MemcachedMinVersion(), "tlsv1.2"},
		{"memcached floor of a TLS 1.3 only profile", modern.MemcachedMinVersion(), "tlsv1.3"},
		{"memcached gets only the pre-1.3 ciphers", intermediate.MemcachedCiphers(), "ECDHE-RSA-AES128-GCM-SHA256"},
		{"memcached gets nothing from a TLS 1.3 only cipher list", modern.MemcachedCiphers(), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
