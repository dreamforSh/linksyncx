// Drives the official OpenAI Node SDK against the gateway test server started
// by TestOpenAINodeSDKAgainstCodexOAuth. Usage: node openai_sdk_e2e.cjs <sdkDir> <baseURL>
// Every case records what the SDK parsed; the Go test asserts on the report.
const OpenAI = require(process.argv[2]);

const client = new OpenAI({ apiKey: 'sk-test', baseURL: process.argv[3], maxRetries: 0, timeout: 20000 });
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
const report = { sdkVersion: require(process.argv[2] + '/package.json').version, cases: {} };

async function run(name, fn) {
  try {
    report.cases[name] = { ok: true, ...(await fn()) };
  } catch (err) {
    report.cases[name] = { ok: false, error: String((err && err.stack) || err) };
  }
}

(async () => {
  await run('responses_text_xhigh', async () => {
    const res = await client.responses.create({
      model: 'gpt-5.4',
      input: 'case=responses_text_xhigh say hello',
      reasoning: { effort: 'xhigh', summary: 'auto' },
    });
    return { id: res.id, status: res.status, outputText: res.output_text, outputTypes: res.output.map((o) => o.type), usage: res.usage };
  });

  await run('responses_stream_image_max', async () => {
    const stream = await client.responses.create({
      model: 'gpt-5.6',
      stream: true,
      reasoning: { effort: 'max' },
      input: [{ role: 'user', content: [
        { type: 'input_text', text: 'case=responses_stream_image_max describe' },
        { type: 'input_image', image_url: PNG, detail: 'high' },
      ] }],
    });
    const types = [];
    let text = '';
    let reasoning = '';
    let completed = null;
    let lastSeq = -1;
    let seqMonotonic = true;
    for await (const event of stream) {
      types.push(event.type);
      if (typeof event.sequence_number === 'number') {
        if (event.sequence_number <= lastSeq) seqMonotonic = false;
        lastSeq = event.sequence_number;
      }
      if (event.type === 'response.output_text.delta') text += event.delta;
      if (event.type === 'response.reasoning_summary_text.delta') reasoning += event.delta;
      if (event.type === 'response.completed') completed = event.response;
    }
    return { types, text, reasoning, seqMonotonic, completedOutputTypes: completed && completed.output.map((o) => o.type), completedText: completed && completed.output.filter((o) => o.type === 'message').map((o) => o.content.map((c) => c.text).join('')).join(''), usage: completed && completed.usage };
  });

  await run('responses_stream_helper', async () => {
    const stream = client.responses.stream({
      model: 'gpt-5.4',
      input: 'case=responses_stream_helper say hello',
      reasoning: { effort: 'xhigh' },
    });
    let deltas = '';
    stream.on('response.output_text.delta', (e) => { deltas += e.delta; });
    const final = await stream.finalResponse();
    return { deltas, outputText: final.output_text, id: final.id };
  });

  await run('responses_retrieve_and_continue', async () => {
    const first = await client.responses.create({ model: 'gpt-5.4', input: 'case=state_first remember 7' });
    const fetched = await client.responses.retrieve(first.id);
    const items = await client.responses.inputItems.list(first.id);
    const second = await client.responses.create({
      model: 'gpt-5.4', previous_response_id: first.id, input: 'case=state_second what number?',
    });
    return { firstID: first.id, fetchedID: fetched.id, fetchedText: fetched.output_text, inputItemCount: items.data.length, secondText: second.output_text, secondPrev: second.previous_response_id };
  });

  await run('responses_stream_function_call', async () => {
    const stream = client.responses.stream({
      model: 'gpt-5.4',
      input: 'case=responses_function_call weather in Paris',
      tools: [{ type: 'function', name: 'get_weather', parameters: { type: 'object', properties: { city: { type: 'string' } }, required: ['city'] } }],
    });
    let argDeltas = '';
    stream.on('response.function_call_arguments.delta', (e) => { argDeltas += e.delta; });
    const final = await stream.finalResponse();
    const call = final.output.find((o) => o.type === 'function_call');
    return { argDeltas, name: call && call.name, callID: call && call.call_id, arguments: call && call.arguments };
  });

  await run('chat_image_xhigh', async () => {
    const res = await client.chat.completions.create({
      model: 'gpt-5.4',
      reasoning_effort: 'xhigh',
      messages: [{ role: 'user', content: [
        { type: 'text', text: 'case=chat_image_xhigh describe' },
        { type: 'image_url', image_url: { url: PNG, detail: 'low' } },
      ] }],
    });
    return { object: res.object, content: res.choices[0].message.content, finish: res.choices[0].finish_reason, usage: res.usage };
  });

  await run('chat_stream_max_usage', async () => {
    const stream = await client.chat.completions.create({
      model: 'gpt-5.6',
      reasoning_effort: 'max',
      stream: true,
      stream_options: { include_usage: true },
      messages: [{ role: 'user', content: 'case=chat_stream_max_usage say hello' }],
    });
    let content = '';
    let finish = null;
    let role = null;
    let usage = null;
    let chunks = 0;
    let usageChunkChoices = null;
    for await (const chunk of stream) {
      chunks++;
      if (chunk.usage) { usage = chunk.usage; usageChunkChoices = chunk.choices.length; }
      for (const choice of chunk.choices) {
        if (choice.delta && choice.delta.role) role = choice.delta.role;
        if (choice.delta && choice.delta.content) content += choice.delta.content;
        if (choice.finish_reason) finish = choice.finish_reason;
      }
    }
    return { content, finish, role, usage, chunks, usageChunkChoices };
  });

  await run('chat_stream_helper_tools', async () => {
    const stream = client.chat.completions.stream({
      model: 'gpt-5.4',
      messages: [{ role: 'user', content: 'case=chat_function_call weather in Paris' }],
      tools: [{ type: 'function', function: { name: 'get_weather', parameters: { type: 'object', properties: { city: { type: 'string' } }, required: ['city'] } } }],
    });
    const final = await stream.finalChatCompletion();
    const call = final.choices[0].message.tool_calls && final.choices[0].message.tool_calls[0];
    return { finish: final.choices[0].finish_reason, name: call && call.function.name, arguments: call && call.function.arguments, callID: call && call.id };
  });

  process.stdout.write('SDK_REPORT ' + JSON.stringify(report) + '\n');
})();
