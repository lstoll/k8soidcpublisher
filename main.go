package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"lds.li/oauth2ext/oauth2as/discovery"
	"lds.li/oauth2ext/oidc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

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

	metadata, err := discoverAPIServerOIDC(ctx, cl)
	if err != nil {
		slog.Error("Failed to discover provider metadata", "error", err)
		os.Exit(1)
	}

	upstreamJWKSURI := metadata.JWKSURI
	metadata.JWKSURI = fmt.Sprintf("%s/.well-known/jwks.json", metadata.Issuer)
	// needed to validate, not used.
	metadata.TokenEndpoint = fmt.Sprintf("%s/nonexistent", metadata.Issuer)
	metadata.AuthorizationEndpoint = fmt.Sprintf("%s/nonexistent", metadata.Issuer)

	discoh, err := discovery.NewOIDCConfigurationHandlerWithJWKSSource(metadata, &k8sAPIJWKSSource{cl: cl, url: upstreamJWKSURI})
	if err != nil {
		slog.Error("Failed to create discovery handler", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/openid-configuration", discoh)
	mux.Handle("GET /.well-known/jwks.json", discoh)

	server := &http.Server{
		Addr:    *listen,
		Handler: mux,
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		slog.Info("listening", "addr", *listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Failed to listen", "error", err)
			os.Exit(1)
		}
	})

	<-ctx.Done()
	slog.Info("Received shutdown signal, initiating graceful shutdown...")

	shutdownCtx := context.WithoutCancel(ctx)
	shutdownCtx, shutdownCancel := context.WithTimeout(shutdownCtx, 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Server shutdown failed", "error", err)
	} else {
		slog.Info("Server shutdown gracefully")
	}

	wg.Wait()
	slog.Info("Application shutdown complete")
}

func discoverAPIServerOIDC(ctx context.Context, cl *rest.RESTClient) (*oidc.ProviderMetadata, error) {
	res := cl.Get().RequestURI("/.well-known/openid-configuration").Do(ctx)

	mdraw, err := res.Raw()
	if err != nil {
		return nil, fmt.Errorf("getting /.well-known/openid-configuration: %v", res.Error())
	}

	md := oidc.ProviderMetadata{}
	if err := json.Unmarshal(mdraw, &md); err != nil {
		return nil, fmt.Errorf("unmarshaling discovery response: %v", err)
	}

	return &md, nil
}

type k8sAPIJWKSSource struct {
	cl  *rest.RESTClient
	url string
}

func (s *k8sAPIJWKSSource) GetJWKS(ctx context.Context) ([]byte, error) {
	res := s.cl.Get().RequestURI(s.url).Do(ctx)

	kraw, err := res.Raw()
	if err != nil {
		return nil, fmt.Errorf("getting %s: %v", s.url, res.Error())
	}

	return kraw, nil
}
