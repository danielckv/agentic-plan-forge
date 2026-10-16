import React from "react";

interface Props {
  name: string;
}

export function App(props: Props): JSX.Element {
  return <div>{props.name}</div>;
}

export const Button = (label: string) => <button>{label}</button>;

function Private() {
  return <span />;
}
