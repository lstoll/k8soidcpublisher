package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/lstoll/oidc"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	fetchInterval = 5 * time.Minute

	mdKey = "md"
	ksKey = "ks"
)

func main() {
	ctx := context.Background()

	var (
		listen     = flag.String("listen", "localhost:8080", "address to listen on")
		kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig file, otherwise will use in-cluster config")
	)
	flag.Parse()

	var config *rest.Config
	if *kubeconfig != "" {
		c, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			log.Fatalf("Error flag config: %v", err)
		}
		config = c
	} else {
		c, err := rest.InClusterConfig()
		if err != nil {
			log.Fatalf("Error creating in cluster configuration: %v", err)
		}
		config = c
	}

	// https://github.com/operator-framework/operator-sdk/issues/1570#issuecomment-842962128
	config.APIPath = "/api"
	config.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
	config.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: scheme.Codecs}

	cl, err := rest.RESTClientFor(config)
	if err != nil {
		log.Fatalf("Error creating rest client: %v", err)
	}

	// warmup, and make sure it'll work
	metadata, jwks, err := discoverAPIServerOIDC(ctx, cl)
	if err != nil {
		log.Fatalf("Failed to discover: %v", err)
	}

	// periodically fetch and cache. Serving just uses cached data so this'll
	// keep it fresh
	go func() {
		for range time.NewTicker(fetchInterval).C {
			log.Print("Discovering..")
			// we don't hard fail here, just fall back to cached
			md, ks, err := discoverAPIServerOIDC(ctx, cl)
			if err != nil {
				log.Printf("Failed to discover: %v", err)
			}
			metadata, jwks = md, ks
		}
	}()

	// TODO - check host header for these, so we can potentially overload the server

	http.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("content-type", "application/json")
		// assume that the issuer is always the root. Could be smarter here
		metadata.JWKSURI = metadata.Issuer + "/jwks"
		if err := json.NewEncoder(w).Encode(metadata); err != nil {
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}
	})

	http.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("content-type", "application/jwk-set+json")
		if err := json.NewEncoder(w).Encode(jwks); err != nil {
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}
	})

	log.Printf("listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

func discoverAPIServerOIDC(ctx context.Context, cl *rest.RESTClient) (*oidc.ProviderMetadata, *jose.JSONWebKeySet, error) {
	res := cl.Get().RequestURI("/.well-known/openid-configuration").Do(ctx)

	mdraw, err := res.Raw()
	if err != nil {
		return nil, nil, fmt.Errorf("getting /.well-known/openid-configuration: %v", res.Error())
	}

	md := oidc.ProviderMetadata{}
	if err := json.Unmarshal(mdraw, &md); err != nil {
		return nil, nil, fmt.Errorf("unmarshaling discovery response: %v", err)
	}

	jwksurl, err := url.Parse(md.JWKSURI)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing jwks url %s: %v", md.JWKSURI, err)
	}

	res = cl.Get().RequestURI(jwksurl.Path).Do(ctx)

	kraw, err := res.Raw()
	if err != nil {
		return nil, nil, fmt.Errorf("getting %s: %v", jwksurl.Path, res.Error())
	}

	ks := jose.JSONWebKeySet{}

	if err := json.Unmarshal(kraw, &ks); err != nil {
		return nil, nil, fmt.Errorf("unmarshaling jwks: %v", err)
	}

	return &md, &ks, nil
}

// cache is a super basic cache, we use for sharing data between the fetcher and
// the server. At some point might be worth using the FS or something
type cache struct {
	data   map[string]interface{}
	dataMu sync.RWMutex
}

func (c *cache) Get(key string) interface{} {
	c.dataMu.RLock()
	defer c.dataMu.RUnlock()
	return c.data[key]
}

func (c *cache) Set(key string, d interface{}) {
	c.dataMu.Lock()
	defer c.dataMu.Unlock()
	c.data[key] = d
}
