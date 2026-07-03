/**
 * api.ts - HTTP 通信模块
 * 负责与 padne Python 后端服务通信
 */

export interface ApiConfig {
  testEndpoint: string;
}

export class PdnApiClient {
  private host: string;
  private port: number;
  private config: ApiConfig;

  constructor(host: string, port: number, config: ApiConfig) {
    this.host = host;
    this.port = port;
    this.config = config;
  }

  /** 检测后端服务是否运行 */
  async checkService(): Promise<boolean> {
    try {
      const url = `http://${this.host}:${this.port}${this.config.testEndpoint}`;
      const response = await eda.sys_ClientUrl.request(url);
      return response.ok;
    } catch {
      return false;
    }
  }

  /** 发送 Gerber 分析请求（multipart: zip + JSON config） */
  async analyzeGerber(gerberBlob: Blob, configJson: string): Promise<any> {
    const formData = new FormData();
    formData.append('gerber', gerberBlob, 'gerber.zip');
    formData.append('config', configJson);

    const url = `http://${this.host}:${this.port}/analyze-gerber`;
    const response = await eda.sys_ClientUrl.request(url, 'POST', formData);

    if (!response.ok) {
      const errorText = await response.text();
      console.error('[PdnApiClient] Gerber HTTP 错误:', response.status, errorText);
      throw new Error(`HTTP 错误: ${response.status} - ${errorText}`);
    }

    return await response.json();
  }

  /** 获取服务 URL */
  getServiceUrl(): string {
    return `http://${this.host}:${this.port}`;
  }
}
