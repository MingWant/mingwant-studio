export type ReferenceImage = {
    id: string;
    name: string;
    type: string;
    dataUrl: string;
    url?: string;
    storageKey?: string;
    bytes?: number;
    source?: {
        kind: "drawing";
        drawingId: string;
        revision: number;
        shapeCount: number;
    };
};
