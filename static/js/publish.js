export const PUBLISH_MODE_PREVIEW = 'preview';
export const PUBLISH_MODE_DIRECT = 'direct';

const STATUS_LABELS = {
    queued: '待機中',
    building: 'ビルド中',
    ready: '確認可能',
    failed: '失敗',
    stale: '更新が必要',
};

export function getPublishMode({ deploymentEnabled = false, deploymentStatus = null } = {}) {
    return deploymentEnabled && deploymentStatus === 'ready'
        ? PUBLISH_MODE_PREVIEW
        : PUBLISH_MODE_DIRECT;
}

export function publishStatusLabel(status) {
    return STATUS_LABELS[status] || '未作成';
}

export function publishConfirmationMessage({ deploymentEnabled = false, deploymentStatus = null } = {}) {
    if (getPublishMode({ deploymentEnabled, deploymentStatus }) === PUBLISH_MODE_PREVIEW) {
        return '確認済みのデプロイプレビューからPRを作成しますか？';
    }
    if (!deploymentEnabled) {
        return '現在の編集内容からPRを作成して公開しますか？';
    }
    return `Deployment Previewは${publishStatusLabel(deploymentStatus)}です。現在の編集内容を直接Publishしますか？`;
}
