package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// resourceCatalogFileService keeps provider drivers physical-path-only while
// exposing stable resource:// references to the application layer.
type resourceCatalogFileService struct {
	inner       interfaces.FileService
	catalog     interfaces.ResourceCatalog
	externalURL string
}

// NewResourceCatalogFileService decorates a physical FileService with stable
// resource registration and resolution.
func NewResourceCatalogFileService(
	inner interfaces.FileService,
	catalog interfaces.ResourceCatalog,
) interfaces.FileService {
	if inner == nil || catalog == nil {
		return inner
	}
	return &resourceCatalogFileService{
		inner:       inner,
		catalog:     catalog,
		externalURL: strings.TrimRight(strings.TrimSpace(os.Getenv("APP_EXTERNAL_URL")), "/"),
	}
}

func (s *resourceCatalogFileService) CheckConnectivity(ctx context.Context) error {
	return s.inner.CheckConnectivity(ctx)
}

func resourceKind(name string) (string, string) {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	kind := "file"
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		kind = "image"
	case strings.HasPrefix(mimeType, "audio/"):
		kind = "audio"
	case strings.HasPrefix(mimeType, "video/"):
		kind = "video"
	}
	return kind, mimeType
}

func (s *resourceCatalogFileService) register(
	ctx context.Context,
	physical string,
	tenantID uint64,
	name string,
	size int64,
	temporary bool,
	contentHash string,
) (string, error) {
	kind, mimeType := resourceKind(name)
	ref, err := s.catalog.Register(ctx, tenantID, physical, interfaces.ResourceRegistration{
		Kind:         kind,
		MimeType:     mimeType,
		OriginalName: filepath.Base(name),
		Size:         size,
		ContentHash:  contentHash,
		Temporary:    temporary,
	})
	if err != nil {
		_ = s.inner.DeleteFile(ctx, physical)
		return "", fmt.Errorf("register stored resource: %w", err)
	}
	return ref, nil
}

func (s *resourceCatalogFileService) SaveFile(
	ctx context.Context,
	file *multipart.FileHeader,
	tenantID uint64,
	knowledgeID string,
) (string, error) {
	physical, err := s.inner.SaveFile(ctx, file, tenantID, knowledgeID)
	if err != nil {
		return "", err
	}
	ref, err := s.register(ctx, physical, tenantID, file.Filename, file.Size, false, "")
	if err != nil {
		return "", err
	}
	if knowledgeID != "" {
		if err := s.catalog.Bind(ctx, ref, types.ResourceOwnerKnowledge, knowledgeID, types.ResourceRelationSourceFile); err != nil {
			_ = s.DeleteFile(ctx, ref)
			return "", fmt.Errorf("bind stored resource: %w", err)
		}
	}
	return ref, nil
}

func (s *resourceCatalogFileService) SaveBytes(
	ctx context.Context,
	data []byte,
	tenantID uint64,
	fileName string,
	temp bool,
) (string, error) {
	physical, err := s.inner.SaveBytes(ctx, data, tenantID, fileName, temp)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return s.register(ctx, physical, tenantID, fileName, int64(len(data)), temp, hex.EncodeToString(sum[:]))
}

func (s *resourceCatalogFileService) resolve(ctx context.Context, value string) (string, bool, error) {
	physical, resource, err := s.catalog.ResolvePath(ctx, value)
	return physical, resource != nil, err
}

func (s *resourceCatalogFileService) GetFile(ctx context.Context, filePath string) (io.ReadCloser, error) {
	physical, _, err := s.resolve(ctx, filePath)
	if err != nil {
		return nil, err
	}
	return s.inner.GetFile(ctx, unwrapBackendScope(physical))
}

func (s *resourceCatalogFileService) GetFileURL(ctx context.Context, filePath string) (string, error) {
	physical, isResource, err := s.resolve(ctx, filePath)
	if err != nil {
		return "", err
	}
	unwrapped := unwrapBackendScope(physical)

	// A resource:// handle resolves to a provider path, but `s.inner` is the
	// process-wide default service — chosen from STORAGE_TYPE at boot, which
	// is unrelated to where the handle actually lives. When the resolved path
	// is an OSS object and the deployment serves its bucket publicly, build
	// the stable unsigned URL straight from the path: those links need no
	// proxy hop, never expire, and are cacheable, unlike the /r/<token>
	// capability URL below (2h TTL, which is exactly how chat images go blank
	// in saved history).
	if ossURL, ok := publicObjectURL(unwrapped); ok {
		return ossURL, nil
	}

	if isResource && s.externalURL != "" {
		token, grantErr := s.catalog.CreateAccessGrant(ctx, filePath, 2*time.Hour)
		if grantErr != nil {
			return "", grantErr
		}
		return s.externalURL + "/r/" + token, nil
	}
	return s.inner.GetFileURL(ctx, unwrapped)
}

// publicObjectURL returns a stable unsigned URL for a provider path when the
// object's bucket is served publicly, and false otherwise.
//
// Only OSS participates today, and only when the deployment opts in with
// OSS_PUBLIC_URL=true (the same switch ossFileService.GetFileURL honours, so
// one bucket yields one URL shape everywhere). The endpoint is not carried in
// the path — `oss://<bucket>/<key>` has no host — so it comes from
// OSS_PUBLIC_URL_ENDPOINT, falling back to the OSS_ENDPOINT already used by
// the env-configured backend.
func publicObjectURL(physical string) (string, bool) {
	if !ossPublicURLEnabled() {
		return "", false
	}
	const ossSchemePrefix = "oss://"
	if !strings.HasPrefix(physical, ossSchemePrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(physical, ossSchemePrefix)
	bucket, key, ok := strings.Cut(rest, "/")
	if !ok || bucket == "" || key == "" {
		return "", false
	}
	endpoint := strings.TrimSpace(os.Getenv("OSS_PUBLIC_URL_ENDPOINT"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("OSS_ENDPOINT"))
	}
	if endpoint == "" {
		return "", false
	}
	return ossPublicURL(endpoint, bucket, key), true
}

// unwrapBackendScope strips the storage://{backendID}/ scope prefix that
// BackendScopedFileService adds when a tenant pins a storage backend, so the
// raw provider driver (oss:// etc.) can parse the path. resource:// references
// resolved through the default file service may point at backend-scoped paths;
// without unwrapping, parseOssFilePath fails with "invalid OSS file path".
func unwrapBackendScope(path string) string {
	if _, inner, ok := types.ParseStorageBackendPath(path); ok {
		return inner
	}
	return path
}

func (s *resourceCatalogFileService) DeleteFile(ctx context.Context, filePath string) error {
	physical, isResource, err := s.resolve(ctx, filePath)
	if err != nil {
		return err
	}
	if err := s.inner.DeleteFile(ctx, physical); err != nil {
		return err
	}
	if isResource {
		return s.catalog.MarkDeleted(ctx, filePath)
	}
	return nil
}

func (s *resourceCatalogFileService) CopyFile(
	ctx context.Context,
	filePath string,
	tenantID uint64,
	knowledgeID string,
) (string, error) {
	physical, _, err := s.resolve(ctx, filePath)
	if err != nil {
		return "", err
	}
	copied, err := s.inner.CopyFile(ctx, physical, tenantID, knowledgeID)
	if err != nil {
		return "", err
	}
	ref, err := s.register(ctx, copied, tenantID, filepath.Base(physical), 0, false, "")
	if err != nil {
		return "", err
	}
	if knowledgeID != "" {
		if err := s.catalog.Bind(ctx, ref, types.ResourceOwnerKnowledge, knowledgeID, types.ResourceRelationSourceFile); err != nil {
			_ = s.DeleteFile(ctx, ref)
			return "", fmt.Errorf("bind copied resource: %w", err)
		}
	}
	return ref, nil
}

var _ interfaces.FileService = (*resourceCatalogFileService)(nil)
