import { useEffect, useRef } from "react";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";

import { layoutPlanGraph } from "../ui/planGraph";
import type { PlanGraph, PlacedNode } from "../ui/planGraph";

/**
 * The plan as a clustered node graph in 3D.
 *
 * This module is the only place three.js is imported, and it is reached solely
 * through a dynamic import, so the library is a separate chunk that a session
 * never downloads unless it opens this view. That matters more here than in a
 * normal web app: the bundle is compiled into the Go binary, so every byte is
 * shipped to every user whether or not they look at it.
 *
 * Nothing in here is unit-tested, because nothing can be — jsdom implements no
 * WebGL, so constructing a renderer throws. The logic worth testing lives in
 * `ui/planGraph.ts` and is exercised there; this file is kept to scene
 * management so there is little left to get wrong.
 */
const KIND_COLOR: Record<string, number> = {
  epic: 0x7aa2f7,
  story: 0x9ece6a,
  workstream: 0xe0af68,
  task: 0xbb9af7,
};
const SEVERITY_COLOR: Record<string, number> = {
  CRITICAL: 0xf7768e,
  HIGH: 0xff9e64,
  MEDIUM: 0xe0af68,
};

export default function PlanGraph3D(props: {
  graph: PlanGraph;
  fresh?: ReadonlySet<string>;
  selectedId?: string | null;
  onSelect: (id: string) => void;
  /** True when the scene fills a dialog: more room, so the camera backs off. */
  expanded?: boolean;
}): React.ReactElement {
  const { graph, fresh, selectedId, onSelect, expanded } = props;
  const mountRef = useRef<HTMLDivElement | null>(null);
  // The click handler changes on every render; the scene is built once. A ref
  // keeps the listener stable while still calling the current handler.
  const selectRef = useRef(onSelect);
  selectRef.current = onSelect;
  const selected = selectedId === null || selectedId === undefined
    ? null
    : graph.nodes.find((n) => n.id === selectedId) ?? null;

  useEffect(() => {
    const mount = mountRef.current;
    if (mount === null) return;

    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(55, 1, 0.1, 500);
    // A dialog gives the plan room to breathe, so the camera starts further
    // back and shows the whole graph instead of the front cluster.
    camera.position.set(0, expanded === true ? 20 : 14, expanded === true ? 38 : 26);

    const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    mount.appendChild(renderer.domElement);

    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableDamping = true;
    controls.target.set(0, -4, 0);

    scene.add(new THREE.AmbientLight(0xffffff, 1.4));
    const key = new THREE.DirectionalLight(0xffffff, 1.1);
    key.position.set(8, 18, 12);
    scene.add(key);

    // One geometry and one material per kind, shared by every node of that
    // kind: a plan with a few hundred nodes would otherwise allocate a few
    // hundred of each and drop frames on a laptop.
    const sphere = new THREE.SphereGeometry(0.62, 20, 16);
    const materials = new Map<string, THREE.MeshStandardMaterial>();
    const materialFor = (node: PlacedNode): THREE.MeshStandardMaterial => {
      const colour = node.severity !== undefined ? SEVERITY_COLOR[node.severity] : KIND_COLOR[node.kind];
      const token = `${colour ?? 0xffffff}`;
      let material = materials.get(token);
      if (material === undefined) {
        material = new THREE.MeshStandardMaterial({ color: colour ?? 0xffffff, roughness: 0.45, metalness: 0.1 });
        materials.set(token, material);
      }
      return material;
    };

    const placed = layoutPlanGraph(graph);
    const meshes: THREE.Mesh[] = [];
    const byId = new Map(placed.map((p) => [p.id, p]));

    for (const node of placed) {
      const mesh = new THREE.Mesh(sphere, materialFor(node));
      mesh.position.set(node.x, node.y, node.z);
      // A node that has just arrived is larger and brighter for a moment, so
      // the plan visibly grows instead of silently gaining a dot.
      const isFresh = fresh?.has(node.id) === true;
      const scale = (node.kind === "epic" ? 1.5 : 1) * (isFresh ? 1.45 : 1);
      mesh.scale.setScalar(scale);
      mesh.userData = { id: node.id, baseScale: scale, fresh: isFresh };
      if (node.id === selectedId) mesh.scale.setScalar(scale * 1.3);
      scene.add(mesh);
      meshes.push(mesh);
    }

    const edgePoints: number[] = [];
    for (const node of placed) {
      if (node.parentId === null) continue;
      const parent = byId.get(node.parentId);
      if (parent === undefined) continue;
      edgePoints.push(parent.x, parent.y, parent.z, node.x, node.y, node.z);
    }
    const edgeGeometry = new THREE.BufferGeometry();
    edgeGeometry.setAttribute("position", new THREE.Float32BufferAttribute(edgePoints, 3));
    const edgeMaterial = new THREE.LineBasicMaterial({ color: 0x5a6384, transparent: true, opacity: 0.55 });
    const edges = new THREE.LineSegments(edgeGeometry, edgeMaterial);
    scene.add(edges);

    const raycaster = new THREE.Raycaster();
    const pointer = new THREE.Vector2();
    const onClick = (event: MouseEvent): void => {
      const rect = renderer.domElement.getBoundingClientRect();
      pointer.x = ((event.clientX - rect.left) / rect.width) * 2 - 1;
      pointer.y = -((event.clientY - rect.top) / rect.height) * 2 + 1;
      raycaster.setFromCamera(pointer, camera);
      const hit = raycaster.intersectObjects(meshes, false)[0];
      if (hit !== undefined) selectRef.current(String(hit.object.userData.id));
    };
    renderer.domElement.addEventListener("click", onClick);

    const resize = (): void => {
      const { clientWidth, clientHeight } = mount;
      if (clientWidth === 0 || clientHeight === 0) return;
      renderer.setSize(clientWidth, clientHeight, false);
      camera.aspect = clientWidth / clientHeight;
      camera.updateProjectionMatrix();
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(mount);

    let frame = 0;
    const tick = (time: number): void => {
      frame = requestAnimationFrame(tick);
      for (const mesh of meshes) {
        if (mesh.userData.fresh !== true) continue;
        // A slow pulse, not a flash: the eye should be drawn to new work
        // without the whole scene shimmering.
        const pulse = 1 + Math.sin(time / 320) * 0.08;
        mesh.scale.setScalar(Number(mesh.userData.baseScale) * pulse);
      }
      controls.update();
      renderer.render(scene, camera);
    };
    frame = requestAnimationFrame(tick);

    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      renderer.domElement.removeEventListener("click", onClick);
      controls.dispose();
      // A WebGL context is a scarce resource; browsers drop the oldest when a
      // page allocates too many. Releasing it on unmount is what lets this
      // view be opened and closed repeatedly.
      renderer.dispose();
      sphere.dispose();
      edgeGeometry.dispose();
      edgeMaterial.dispose();
      for (const material of materials.values()) material.dispose();
      mount.removeChild(renderer.domElement);
    };
  }, [graph, fresh, selectedId, expanded]);

  return (
    <div className="pf-plangraph3d-wrap" data-expanded={expanded === true ? "yes" : undefined}>
      <div className="pf-plangraph3d" ref={mountRef} role="img" aria-label="Plan graph in three dimensions" />
      {selected !== null && (
        <aside className="pf-plangraph3d-detail" aria-label={`Details for ${selected.label}`}>
          <span className="pf-plandocs-kind">{selected.kind}</span>
          <h3 className="pf-plangraph3d-title">{selected.label}</h3>
          {selected.severity !== undefined && (
            <p className="pf-plandocs-detail-severity">{selected.severity} finding</p>
          )}
          {selected.detail !== "" && <p className="pf-plangraph3d-text">{selected.detail}</p>}
          <p className="pf-caption pf-mono">{selected.id}</p>
        </aside>
      )}
    </div>
  );
}
