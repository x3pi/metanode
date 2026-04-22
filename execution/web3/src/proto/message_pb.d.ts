import * as jspb from 'google-protobuf'



export class Header extends jspb.Message {
  getCommand(): string;
  setCommand(value: string): Header;

  getPubkey(): Uint8Array | string;
  getPubkey_asU8(): Uint8Array;
  getPubkey_asB64(): string;
  setPubkey(value: Uint8Array | string): Header;

  getToaddress(): Uint8Array | string;
  getToaddress_asU8(): Uint8Array;
  getToaddress_asB64(): string;
  setToaddress(value: Uint8Array | string): Header;

  getSign(): Uint8Array | string;
  getSign_asU8(): Uint8Array;
  getSign_asB64(): string;
  setSign(value: Uint8Array | string): Header;

  getVersion(): string;
  setVersion(value: string): Header;

  getId(): string;
  setId(value: string): Header;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): Header.AsObject;
  static toObject(includeInstance: boolean, msg: Header): Header.AsObject;
  static serializeBinaryToWriter(message: Header, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): Header;
  static deserializeBinaryFromReader(message: Header, reader: jspb.BinaryReader): Header;
}

export namespace Header {
  export type AsObject = {
    command: string,
    pubkey: Uint8Array | string,
    toaddress: Uint8Array | string,
    sign: Uint8Array | string,
    version: string,
    id: string,
  }
}

export class Message extends jspb.Message {
  getHeader(): Header | undefined;
  setHeader(value?: Header): Message;
  hasHeader(): boolean;
  clearHeader(): Message;

  getBody(): Uint8Array | string;
  getBody_asU8(): Uint8Array;
  getBody_asB64(): string;
  setBody(value: Uint8Array | string): Message;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): Message.AsObject;
  static toObject(includeInstance: boolean, msg: Message): Message.AsObject;
  static serializeBinaryToWriter(message: Message, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): Message;
  static deserializeBinaryFromReader(message: Message, reader: jspb.BinaryReader): Message;
}

export namespace Message {
  export type AsObject = {
    header?: Header.AsObject,
    body: Uint8Array | string,
  }
}

