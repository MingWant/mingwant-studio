import { describe, expect, test } from "bun:test";
import { confirmsProviderBillingUncertain, confirmsProviderWasNotCalled } from "../src/lib/provider-request-error";
import { onlineAgentFailureMessage } from "../src/lib/agent-error";

describe("provider request error boundaries", () => {
    test("保留带请求编号的服务端费用待核对说明", () => {
        const message = "自定义渠道上游已返回成功状态，但响应无法完整交付；模型请求可能已经执行并产生费用，请勿立即重试，请先核对供应商后台或账单（请求编号：req-123）";
        expect(confirmsProviderBillingUncertain(message)).toBe(true);
        expect(confirmsProviderBillingUncertain("上游网关超时（524）：模型可能仍在服务端执行并产生费用，请勿立即重试")).toBe(true);
    });

    test("普通 5xx 文本不会被误认成可信费用结论", () => {
        expect(confirmsProviderBillingUncertain("temporary gateway failure")).toBe(false);
        expect(confirmsProviderBillingUncertain("请求失败，请稍后重试")).toBe(false);
    });

    test("调用前证明仍优先识别为无供应商费用", () => {
        const message = "自定义渠道请求在调用供应商前超时：本次没有发出上游请求";
        expect(confirmsProviderWasNotCalled(message)).toBe(true);
        expect(confirmsProviderBillingUncertain(message)).toBe(false);
    });

    test("系统渠道创建计费订单前超时不应被识别为费用不确定", () => {
        const message = "系统渠道请求在调用供应商前超时：本次尚未创建计费订单或发出上游请求";
        expect(confirmsProviderWasNotCalled(message)).toBe(true);
        expect(confirmsProviderBillingUncertain(message)).toBe(false);
    });

    test("在线 Agent 对流交付失败保留费用待核对提示", () => {
        const message = onlineAgentFailureMessage(new Error("上游成功但响应交付失败：请求状态不确定且可能已经计费，请勿立即重试"), 1);
        expect(message).toContain("请求状态不确定");
        expect(message).toContain("本轮已停止");
        expect(message.match(/可能已经计费/g)?.length || 0).toBe(1);
    });

    test("在线 Agent 对调用前超时不追加费用风险", () => {
        const message = onlineAgentFailureMessage(new Error("自定义渠道请求在调用供应商前超时：本次没有发出上游请求"), 1);
        expect(message).not.toContain("可能仍在供应商服务端执行并产生费用");
        expect(message).toContain("没有发出该次上游请求");
        expect(message).toContain("不会产生该次供应商费用");
        expect(message).toContain("本轮已停止");
    });
});
