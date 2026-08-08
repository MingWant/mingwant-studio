import { uploadMediaFile, type UploadedFile } from "@/services/file-storage";

export type VideoGenerationResult = { blob?: Blob; url?: string; mimeType?: string };

/** 模型调用与轮询由后端持久化任务负责；浏览器这里只保存已经完成的结果。 */
export async function storeGeneratedVideo(result: VideoGenerationResult): Promise<UploadedFile> {
    if (result.blob) return uploadMediaFile(result.blob, "video");
    if (result.url) return { url: result.url, storageKey: "", bytes: 0, mimeType: result.mimeType || "video/mp4" };
    throw new Error("视频接口没有返回可播放的视频");
}
