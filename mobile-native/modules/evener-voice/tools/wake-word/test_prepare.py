"""Fast converter regression tests, with no downloaded data or model required."""
import os
from pathlib import Path
import tempfile
import unittest

import numpy as np
import onnx
from onnx import TensorProto, helper, numpy_helper
import onnxruntime as ort

from prepare import compact


class CompactModelTests(unittest.TestCase):
    def test_log_floor_survives_weight_compaction(self):
        # The real mel frontend clips power to 1e-10 before log. Packing that
        # scalar as FP16 makes it zero, turning silence into -inf and then NaN.
        graph = helper.make_graph([
            helper.make_node("MatMul", ["audio", "weights"], ["power"]),
            helper.make_node("Clip", ["power", "floor"], ["clipped"]),
            helper.make_node("Log", ["clipped"], ["mel"]),
        ], "log_floor", [helper.make_tensor_value_info("audio", TensorProto.FLOAT, [1, 2])],
            [helper.make_tensor_value_info("mel", TensorProto.FLOAT, [1, 2])], [
                numpy_helper.from_array(np.ones((2, 2), np.float32), "weights"),
                numpy_helper.from_array(np.array(1e-10, np.float32), "floor"),
            ])
        model = helper.make_model(graph, opset_imports=[helper.make_opsetid("", 13)], ir_version=8)
        with tempfile.TemporaryDirectory(dir=os.environ.get("EVENER_SCRATCH_DIR")) as work:
            original, packed = Path(work) / "original.onnx", Path(work) / "packed.onnx"
            onnx.save(model, original)
            compact(original, packed)
            options = ort.SessionOptions()
            options.intra_op_num_threads = 1
            options.inter_op_num_threads = 1
            runtime = ort.InferenceSession(str(packed), options, providers=["CPUExecutionProvider"])
            actual = runtime.run(None, {"audio": np.zeros((1, 2), np.float32)})[0]
            self.assertTrue(np.isfinite(actual).all())
            np.testing.assert_allclose(actual, np.full((1, 2), np.log(1e-10)), rtol=1e-6)
            self.assertEqual(runtime.get_inputs()[0].type, "tensor(float)")
            self.assertEqual(runtime.get_outputs()[0].type, "tensor(float)")


if __name__ == "__main__":
    unittest.main()
